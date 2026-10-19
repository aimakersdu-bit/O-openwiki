import assert from 'node:assert';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { runLinker } from '../linker.js';

async function testLinker() {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'linker_test_'));

  try {
    // 1. Create simulated microservices
    const serviceADir = path.join(tmpDir, 'order-service');
    const serviceBDir = path.join(tmpDir, 'payment-service');
    const gatewayDir = path.join(tmpDir, 'api-gateway');

    fs.mkdirSync(serviceADir, { recursive: true });
    fs.mkdirSync(serviceBDir, { recursive: true });
    fs.mkdirSync(gatewayDir, { recursive: true });

    // Gateway calls order-service via HTTP
    fs.writeFileSync(
      path.join(gatewayDir, 'router.go'),
      `package main
func setupRoutes(r *gin.Engine) {
    r.GET("/api/v1/portal/orders", handleOrders)
    // Forward to order service
    http.Get("http://order-service/api/v1/orders/create")
}`
    );

    // Order Service provides /api/v1/orders/create and produces MQ: ORDER_CREATED
    fs.writeFileSync(
      path.join(serviceADir, 'OrderController.java'),
      `package com.bank.order;
@RestController
@RequestMapping("/api/v1/orders")
public class OrderController {
    @PostMapping("/create")
    public Result createOrder() {
        kafkaTemplate.send("ORDER_CREATED_TOPIC", orderDto);
        return Result.ok();
    }
}`
    );

    // Payment Service consumes MQ: ORDER_CREATED and provides /api/v1/pay/process
    fs.writeFileSync(
      path.join(serviceBDir, 'PaymentConsumer.java'),
      `package com.bank.payment;
@Component
public class PaymentConsumer {
    @KafkaListener(topics = "ORDER_CREATED_TOPIC")
    public void onOrderCreated(String msg) {
        processPayment(msg);
    }
    @PostMapping("/api/v1/pay/process")
    public Result pay() {}
}`
    );

    // 2. Run Linker
    const result = await runLinker(tmpDir);

    // 3. Verify graph outputs
    assert.ok(result.nodes.length >= 3, `Expected at least 3 nodes, got ${result.nodes.length}`);
    assert.ok(result.edges.length >= 2, `Expected at least 2 edges, got ${result.edges.length}`);

    // Verify MQ Edge: order-service -> payment-service
    const mqEdge = result.edges.find(e => e.type === 'MQ' && e.source === 'order-service' && e.target === 'payment-service');
    assert.ok(mqEdge, 'Expected MQ edge from order-service to payment-service');
    assert.strictEqual(mqEdge.label, 'ORDER_CREATED_TOPIC');

    // Verify files created
    const graphJsonPath = path.join(tmpDir, '_project_hub', 'cross_repo_graph.json');
    assert.ok(fs.existsSync(graphJsonPath), 'cross_repo_graph.json must exist');

    const topologyMdPath = path.join(tmpDir, '_project_hub', 'openwiki', 'architecture', 'topology.md');
    assert.ok(fs.existsSync(topologyMdPath), 'topology.md must exist');

    const topologyContent = fs.readFileSync(topologyMdPath, 'utf-8');
    assert.ok(topologyContent.includes('```mermaid'), 'topology.md must contain mermaid diagram');
    assert.ok(topologyContent.includes('ORDER_CREATED_TOPIC'), 'topology.md must mention topic');

    console.log('✅ Linker unit test passed successfully!');
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
}

testLinker().catch((err) => {
  console.error('❌ Linker test failed:', err);
  process.exit(1);
});
