function bindings(opts) {
  return {};
}
bindings.getRoot = function getRoot() {
  return process.cwd();
};
bindings.getFileName = function getFileName() {
  return process.cwd();
};
module.exports = exports = bindings;
