# Pure Go DHCP Implementation

This directory contains pure Go DHCP client and relay implementations using the [insomniacslk/dhcp](https://github.com/insomniacslk/dhcp) library.

## Features

### DHCPv4 Client (`dhcp4_client.go`)
- Full DORA (Discover-Offer-Request-Ack) handshake
- Unicast renewal to server
- Release and Decline messages
- DHCPINFORM for configuration-only requests
- Support for:
  - Option 12: Hostname
  - Option 61: Client Identifier
  - Option 121: Classless Static Routes
  - Option 42: NTP Servers
  - Option 26: MTU
  - Custom options

### DHCPv6 Client (`dhcp6_client.go`)
- Full 4-way handshake (Solicit → Advertise → Request → Reply)
- Rapid Commit (2-way: Solicit → Reply)
- IA_NA (Non-temporary Address)
- IA_PD (Prefix Delegation)
- Renew, Rebind, Release, Decline
- Information-Request (stateless DHCPv6)
- Support for:
  - DNS Recursive Name Server
  - Domain Search List
  - SNTP Server List
  - Boot File URL

### DHCP Relay (`dhcp_relay.go`)
- DHCPv4 Relay with Option 82 (Relay Agent Information)
  - Sub-option 1: Circuit ID
  - Sub-option 2: Remote ID
- DHCPv6 Relay-Forward/Reply encapsulation
  - Interface ID option
  - Remote ID option
- Hop count enforcement

## Status

The pure-Go **clients** (`dhcp4_client.go`, `dhcp6_client.go`) are **compiled by
default** and wired into the main DHCP path: `netlink.DHCPManager` tries the
pure-Go client first and falls back to an external client (dhclient/dhcpcd/udhcpc)
only if the pure-Go path fails. `RenewDHCPv4` performs a real T1 unicast renewal
via the pure-Go client. No build tag is needed for the clients.

The **relay** (`dhcp_relay.go`) is still behind the `purego` build tag and is
**not compiled by default** — it has API drift against the current
insomniacslk/dhcp release and `RelayServer.Run()` is only a skeleton. See TODO
P2-4 for relay completion.

### Dependency

```
github.com/insomniacslk/dhcp   // added to go.mod (requires Go 1.23+)
```

### Integration (already done)

`netlink/dhcp.go` dispatches to the pure-Go client first:

```go
func (m *DHCPManager) RequestDHCPv4WithContext(ctx context.Context, ifaceName string) (*DHCPv4Lease, error) {
    lease, err := m.requestDHCPv4PureGo(ctx, ifaceName) // 首选纯 Go
    if err == nil {
        return lease, nil
    }
    if m.externalV4 == "" {
        return nil, err
    }
    return m.requestDHCPv4External(ctx, ifaceName) // 回退外部客户端
}
```

## Usage Examples

### DHCPv4 Client

```go
client, _ := purego.NewDHCPv4Client("eth0")
client.SetHostname("myhost")
client.SetTimeout(10 * time.Second)

// Request lease
lease, err := client.Request(ctx)
fmt.Printf("Got IP: %s, Gateway: %s\n", lease.IP, lease.Gateway)

// Renew
newLease, _ := client.Renew(ctx, lease)

// Release
client.Release(ctx, lease)
```

### DHCPv6 Client

```go
client, _ := purego.NewDHCPv6Client("eth0")
client.SetRapidCommit(true)  // Use 2-way handshake
client.SetRequestPD(true, 56) // Request /56 prefix

lease, err := client.Request(ctx)
fmt.Printf("Got addresses: %v\n", lease.Addresses)
fmt.Printf("Got prefixes: %v\n", lease.Prefixes)
```

### DHCPv4 Relay

```go
config := &purego.DHCPv4RelayConfig{
    ListenAddr:  net.ParseIP("192.168.1.1"),
    ServerAddrs: []net.IP{net.ParseIP("10.0.0.1")},
    GatewayIP:   net.ParseIP("192.168.1.1"),
    CircuitID:   "eth0",
    RemoteID:    "relay1",
}
relay := purego.NewDHCPv4Relay(config)

// Relay client request to server
relayed, _ := relay.RelayToServer(clientPacket, clientAddr)

// Relay server response to client
response := relay.RelayToClient(serverPacket)
```

## Dependencies

- `github.com/insomniacslk/dhcp` - DHCP protocol implementation
- `github.com/mdlayher/packet` - Raw packet socket
- `github.com/u-root/uio` - I/O utilities

## License

Apache License 2.0
