import {
  BaseCheckpointSaver,
  TASKS,
  WRITES_IDX_MAP,
  copyCheckpoint,
  maxChannelVersion,
} from "@langchain/langgraph-checkpoint";

function getDatabaseClass() {
  const { Database } = require("bun:sqlite");
  return Database;
}

function prepareSql(db: any, checkpointId: boolean) {
  const sql = `
  SELECT
    thread_id,
    checkpoint_ns,
    checkpoint_id,
    parent_checkpoint_id,
    type,
    checkpoint,
    metadata,
    (
      SELECT
        json_group_array(
          json_object(
            'task_id', pw.task_id,
            'channel', pw.channel,
            'type', pw.type,
            'value', CAST(pw.value AS TEXT)
          )
        )
      FROM writes as pw
      WHERE pw.thread_id = checkpoints.thread_id
        AND pw.checkpoint_ns = checkpoints.checkpoint_ns
        AND pw.checkpoint_id = checkpoints.checkpoint_id
    ) as pending_writes,
    (
      SELECT
        json_group_array(
          json_object(
            'type', ps.type,
            'value', CAST(ps.value AS TEXT)
          )
        )
      FROM writes as ps
      WHERE ps.thread_id = checkpoints.thread_id
        AND ps.checkpoint_ns = checkpoints.checkpoint_ns
        AND ps.checkpoint_id = checkpoints.parent_checkpoint_id
        AND ps.channel = '${TASKS}'
      ORDER BY ps.idx
    ) as pending_sends
  FROM checkpoints
  WHERE thread_id = ? AND checkpoint_ns = ? ${
    checkpointId ? "AND checkpoint_id = ?" : "ORDER BY checkpoint_id DESC LIMIT 1"
  }`;
  return db.prepare(sql);
}

export class SqliteSaver extends BaseCheckpointSaver {
  db: any;
  isSetup: boolean;
  withoutCheckpoint: any;
  withCheckpoint: any;

  constructor(db: any, serde?: any) {
    super(serde);
    this.db = db;
    this.isSetup = false;
  }

  static fromConnString(connStringOrLocalPath: string): SqliteSaver {
    const DatabaseClass = getDatabaseClass();
    return new SqliteSaver(new DatabaseClass(connStringOrLocalPath));
  }

  setup(): void {
    if (this.isSetup) return;
    if (typeof this.db.pragma === "function") {
      this.db.pragma("journal_mode=WAL");
    } else {
      this.db.exec("PRAGMA journal_mode=WAL;");
    }
    this.db.exec(`
CREATE TABLE IF NOT EXISTS checkpoints (
  thread_id TEXT NOT NULL,
  checkpoint_ns TEXT NOT NULL DEFAULT '',
  checkpoint_id TEXT NOT NULL,
  parent_checkpoint_id TEXT,
  type TEXT,
  checkpoint BLOB,
  metadata BLOB,
  PRIMARY KEY (thread_id, checkpoint_ns, checkpoint_id)
);`);
    this.db.exec(`
CREATE TABLE IF NOT EXISTS writes (
  thread_id TEXT NOT NULL,
  checkpoint_ns TEXT NOT NULL DEFAULT '',
  checkpoint_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  idx INTEGER NOT NULL,
  channel TEXT NOT NULL,
  type TEXT,
  value BLOB,
  PRIMARY KEY (thread_id, checkpoint_ns, checkpoint_id, task_id, idx)
);`);
    this.withoutCheckpoint = prepareSql(this.db, false);
    this.withCheckpoint = prepareSql(this.db, true);
    this.isSetup = true;
  }

  async getTuple(config: any): Promise<any> {
    this.setup();
    const { thread_id, checkpoint_ns = "", checkpoint_id } =
      config.configurable ?? {};
    const args = [thread_id, checkpoint_ns];
    if (checkpoint_id) args.push(checkpoint_id);
    const row = (
      checkpoint_id ? this.withCheckpoint : this.withoutCheckpoint
    ).get(...args);
    if (!row) return undefined;
    let finalConfig = config;
    if (!checkpoint_id)
      finalConfig = {
        configurable: {
          thread_id: row.thread_id,
          checkpoint_ns,
          checkpoint_id: row.checkpoint_id,
        },
      };
    if (
      finalConfig.configurable?.thread_id === undefined ||
      finalConfig.configurable?.checkpoint_id === undefined
    )
      throw new Error("Missing thread_id or checkpoint_id");
    const pendingWrites = await Promise.all(
      JSON.parse(row.pending_writes).map(async (write: any) => {
        return [
          write.task_id,
          write.channel,
          await this.serde.loadsTyped(write.type ?? "json", write.value ?? ""),
        ];
      }),
    );
    const checkpoint = await this.serde.loadsTyped(
      row.type ?? "json",
      row.checkpoint,
    );
    if (checkpoint.v < 4 && row.parent_checkpoint_id != null)
      await this.migratePendingSends(
        checkpoint,
        row.thread_id,
        row.parent_checkpoint_id,
      );
    return {
      checkpoint,
      config: finalConfig,
      metadata: await this.serde.loadsTyped(row.type ?? "json", row.metadata),
      parentConfig: row.parent_checkpoint_id
        ? {
            configurable: {
              thread_id: row.thread_id,
              checkpoint_ns,
              checkpoint_id: row.parent_checkpoint_id,
            },
          }
        : undefined,
      pendingWrites,
    };
  }

  async *list(config: any, options?: any): AsyncGenerator<any, void, unknown> {
    const { limit, before, filter } = options ?? {};
    this.setup();
    const thread_id = config.configurable?.thread_id;
    const checkpoint_ns = config.configurable?.checkpoint_ns;
    let sql = `
      SELECT
        thread_id,
        checkpoint_ns,
        checkpoint_id,
        parent_checkpoint_id,
        type,
        checkpoint,
        metadata,
        (
          SELECT
            json_group_array(
              json_object(
                'task_id', pw.task_id,
                'channel', pw.channel,
                'type', pw.type,
                'value', CAST(pw.value AS TEXT)
              )
            )
          FROM writes as pw
          WHERE pw.thread_id = checkpoints.thread_id
            AND pw.checkpoint_ns = checkpoints.checkpoint_ns
            AND pw.checkpoint_id = checkpoints.checkpoint_id
        ) as pending_writes,
        (
          SELECT
            json_group_array(
              json_object(
                'type', ps.type,
                'value', CAST(ps.value AS TEXT)
              )
            )
          FROM writes as ps
          WHERE ps.thread_id = checkpoints.thread_id
            AND ps.checkpoint_ns = checkpoints.checkpoint_ns
            AND ps.checkpoint_id = checkpoints.parent_checkpoint_id
            AND ps.channel = '${TASKS}'
          ORDER BY ps.idx
        ) as pending_sends
      FROM checkpoints\n`;
    const whereClause = [];
    if (thread_id) whereClause.push("thread_id = ?");
    if (checkpoint_ns !== undefined && checkpoint_ns !== null)
      whereClause.push("checkpoint_ns = ?");
    if (before?.configurable?.checkpoint_id !== undefined)
      whereClause.push("checkpoint_id < ?");
    const sanitizedFilter = Object.fromEntries(
      Object.entries(filter ?? {}).filter(([, value]) => value !== undefined),
    );
    whereClause.push(
      ...Object.keys(sanitizedFilter).map(
        () => `jsonb(CAST(metadata AS TEXT))->? = ?`,
      ),
    );
    if (whereClause.length > 0)
      sql += `WHERE\n  ${whereClause.join(" AND\n  ")}\n`;
    sql += "\nORDER BY checkpoint_id DESC";
    if (limit) sql += ` LIMIT ${parseInt(limit, 10)}`;
    const args = [
      thread_id,
      checkpoint_ns,
      before?.configurable?.checkpoint_id,
      ...Object.entries(sanitizedFilter).flatMap(([key, value]) => [
        `$.${key}`,
        JSON.stringify(value),
      ]),
    ].filter((value) => value !== undefined && value !== null);
    const rows = this.db.prepare(sql).all(...args);
    if (rows)
      for (const row of rows) {
        const pendingWrites = await Promise.all(
          JSON.parse(row.pending_writes).map(async (write: any) => {
            return [
              write.task_id,
              write.channel,
              await this.serde.loadsTyped(
                write.type ?? "json",
                write.value ?? "",
              ),
            ];
          }),
        );
        const checkpoint = await this.serde.loadsTyped(
          row.type ?? "json",
          row.checkpoint,
        );
        if (checkpoint.v < 4 && row.parent_checkpoint_id != null)
          await this.migratePendingSends(
            checkpoint,
            row.thread_id,
            row.parent_checkpoint_id,
          );
        yield {
          config: {
            configurable: {
              thread_id: row.thread_id,
              checkpoint_ns: row.checkpoint_ns,
              checkpoint_id: row.checkpoint_id,
            },
          },
          checkpoint,
          metadata: await this.serde.loadsTyped(
            row.type ?? "json",
            row.metadata,
          ),
          parentConfig: row.parent_checkpoint_id
            ? {
                configurable: {
                  thread_id: row.thread_id,
                  checkpoint_ns: row.checkpoint_ns,
                  checkpoint_id: row.parent_checkpoint_id,
                },
              }
            : undefined,
          pendingWrites,
        };
      }
  }

  async put(config: any, checkpoint: any, metadata: any): Promise<any> {
    this.setup();
    if (!config.configurable)
      throw new Error("Empty configuration supplied.");
    const thread_id = config.configurable?.thread_id;
    const checkpoint_ns = config.configurable?.checkpoint_ns ?? "";
    const parent_checkpoint_id = config.configurable?.checkpoint_id;
    if (!thread_id)
      throw new Error(
        `Missing "thread_id" field in passed "config.configurable".`,
      );
    const preparedCheckpoint = copyCheckpoint(checkpoint);
    const [[type1, serializedCheckpoint], [type2, serializedMetadata]] =
      await Promise.all([
        this.serde.dumpsTyped(preparedCheckpoint),
        this.serde.dumpsTyped(metadata),
      ]);
    if (type1 !== type2)
      throw new Error(
        "Failed to serialized checkpoint and metadata to the same type.",
      );
    const row = [
      thread_id,
      checkpoint_ns,
      checkpoint.id,
      parent_checkpoint_id,
      type1,
      serializedCheckpoint,
      serializedMetadata,
    ];
    this.db
      .prepare(
        `INSERT OR REPLACE INTO checkpoints (thread_id, checkpoint_ns, checkpoint_id, parent_checkpoint_id, type, checkpoint, metadata) VALUES (?, ?, ?, ?, ?, ?, ?)`,
      )
      .run(...row);
    return {
      configurable: {
        thread_id,
        checkpoint_ns,
        checkpoint_id: checkpoint.id,
      },
    };
  }

  async putWrites(config: any, writes: any[], taskId: string): Promise<void> {
    this.setup();
    if (!config.configurable)
      throw new Error("Empty configuration supplied.");
    if (!config.configurable?.thread_id)
      throw new Error("Missing thread_id field in config.configurable.");
    if (!config.configurable?.checkpoint_id)
      throw new Error("Missing checkpoint_id field in config.configurable.");
    const allSpecial = writes.every(([channel]) => channel in WRITES_IDX_MAP);
    const stmt = this.db.prepare(`
      INSERT ${allSpecial ? "OR REPLACE" : "OR IGNORE"} INTO writes
      (thread_id, checkpoint_ns, checkpoint_id, task_id, idx, channel, type, value)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    `);

    const rowsToRun = await Promise.all(
      writes.map(async (write, idx) => {
        const [type, serializedWrite] = await this.serde.dumpsTyped(write[1]);
        return [
          config.configurable?.thread_id,
          config.configurable?.checkpoint_ns,
          config.configurable?.checkpoint_id,
          taskId,
          (WRITES_IDX_MAP as any)[write[0]] ?? idx,
          write[0],
          type,
          serializedWrite,
        ];
      }),
    );

    if (typeof this.db.transaction === "function") {
      this.db.transaction((rows: any[]) => {
        for (const row of rows) stmt.run(...row);
      })(rowsToRun);
    } else {
      for (const row of rowsToRun) stmt.run(...row);
    }
  }

  async deleteThread(threadId: string): Promise<void> {
    const runDelete = () => {
      this.db
        .prepare(`DELETE FROM checkpoints WHERE thread_id = ?`)
        .run(threadId);
      this.db.prepare(`DELETE FROM writes WHERE thread_id = ?`).run(threadId);
    };
    if (typeof this.db.transaction === "function") {
      this.db.transaction(runDelete)();
    } else {
      runDelete();
    }
  }

  async migratePendingSends(
    checkpoint: any,
    threadId: string,
    parentCheckpointId: string,
  ): Promise<void> {
    const row = this.db
      .prepare(
        `
          SELECT
            checkpoint_id,
            json_group_array(
              json_object(
                'type', ps.type,
                'value', CAST(ps.value AS TEXT)
              )
            ) as pending_sends
          FROM writes as ps
          WHERE ps.thread_id = ?
            AND ps.checkpoint_id = ?
            AND ps.channel = '${TASKS}'
          ORDER BY ps.idx
        `,
      )
      .get(threadId, parentCheckpointId);
    const { pending_sends } = row ?? {};
    if (!pending_sends) return;
    const mutableCheckpoint = checkpoint;
    mutableCheckpoint.channel_values ??= {};
    mutableCheckpoint.channel_values[TASKS] = await Promise.all(
      JSON.parse(pending_sends).map(({ type, value }: any) =>
        this.serde.loadsTyped(type, value),
      ),
    );
    mutableCheckpoint.channel_versions[TASKS] =
      Object.keys(checkpoint.channel_versions).length > 0
        ? maxChannelVersion(...(Object.values(checkpoint.channel_versions) as any[]))
        : this.getNextVersion(undefined);
  }
}
