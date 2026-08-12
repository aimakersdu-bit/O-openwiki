function safeBindings(opts) {
  return {};
}
safeBindings.getRoot = function getRoot() {
  return process.cwd();
};
safeBindings.getFileName = function getFileName() {
  return process.cwd();
};
module.exports = exports = safeBindings;
