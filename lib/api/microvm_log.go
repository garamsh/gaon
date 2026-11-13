package api

// MaxLogTailBytes is the largest value of the log query's tailBytes: the
// most output the guest keeps for a command (ADR 0043).
const MaxLogTailBytes = 16 << 20
