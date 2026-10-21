// Package stream implements the multiplexed loopback WebSocket bus (spec 01).
//
// Wire format: binary frames [u8 kind][u32 BE chanId][payload].
//
//	open=1  payload = channel name (UTF-8)
//	data=2  payload = raw bytes
//	close=3 payload = empty
//	ack=4   payload = u32 BE count of payload bytes consumed (data+json frames)
//	json=5  payload = UTF-8 JSON
//
// Channel ids: client-initiated channels use odd ids chosen by the client
// (scoped to that connection); server-initiated channels (Bus.Open) use even
// ids allocated by the server and are announced to every connection, including
// ones that connect later. Flow control: each side may have at most Window
// (256KB) of unacked data+json payload bytes in flight per channel per
// connection; the receiver acks what it has consumed.
//
// Server-initiated channels have no replay: frames sent while no client is
// connected are dropped (per-channel seq resume is a later feature).
package stream
