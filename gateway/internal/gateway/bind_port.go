package gateway

// firstUnprivilegedPort is the lowest port a process with no capability binds
// where net.ipv4.ip_unprivileged_port_start has the kernel default.
const firstUnprivilegedPort = 1024

// privilegedShift is what BindPort adds to a port below firstUnprivilegedPort.
const privilegedShift = 10000

// BindPort returns the port the proxy listens on for a listener of port p. The
// proxy runs as uid 65532 with no capability, so it does not bind a port below
// 1024: it listens on that port plus 10000, and the Service and the MicroVM's
// named port carry the listener's port to it. Any other port is bound as it
// is.
func BindPort(p int32) int32 {
	if p < firstUnprivilegedPort {
		return p + privilegedShift
	}
	return p
}
