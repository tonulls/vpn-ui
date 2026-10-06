package xray

// Traffic represents network traffic statistics for Xray connections.
// It tracks upload and download bytes for inbound or outbound traffic.
type Traffic struct {
	IsInbound  bool
	IsOutbound bool
	Tag        string
	Up         int64
	Down       int64
}

// MembershipTraffic — точный расход одного inbound и одного аккаунта.
// IdentityType равен "email" для обычных Xray users и "wireguard-ip" для peer Xray-WireGuard.
type MembershipTraffic struct {
	InboundTag   string
	IdentityType string
	Identity     string
	Up           int64
	Down         int64
}
