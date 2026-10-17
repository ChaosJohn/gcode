// Package transporterr ports jcode-provider-core's shared transient transport
// classifier: transport-level faults worth a fresh-connection retry.
package transporterr

import "strings"

// IsTransientTransportError reports whether an error string describes a
// transport-level fault that a retry on a fresh connection can fix. Provider
// API errors (4xx/5xx bodies) are classified elsewhere.
func IsTransientTransportError(errorStr string) bool {
	lower := strings.ToLower(errorStr)
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

var markers = []string{
	"connection reset",
	"connection closed",
	"connection refused",
	"connection aborted",
	"broken pipe",
	"timed out",
	"timeout",
	"operation timed out",
	"error decoding",
	"error reading",
	"unexpected eof",
	// rustls reports an abrupt TCP close mid-stream as "peer closed connection
	// without sending TLS close_notify"; the docs URL spells it "unexpected-eof".
	"close_notify",
	"peer closed connection",
	"tls handshake eof",
	// reqwest/hyper wrap connect-phase failures as "client error (Connect)"
	// and connection-level faults as "connection error: ...".
	"client error (connect)",
	"connection error",
	// hyper h1: connection closed while a response was still incomplete.
	"incomplete message",
	"request or response body error",
	"badrecordmac",
	"bad_record_mac",
	"fatal alert: badrecordmac",
	"fatal alert: bad_record_mac",
	"received fatal alert: badrecordmac",
	"received fatal alert: bad_record_mac",
	"decryption failed or bad record mac",
	"temporary failure in name resolution",
	"failed to lookup address information",
	"dns error",
	"name or service not known",
	"no route to host",
	"network is unreachable",
	"host is unreachable",
	// HTTP/2 faults on reused/multiplexed connections: RST_STREAM / GOAWAY.
	"http2 error",
	"stream error",
	"stream_read_error",
	"protocol error",
	"refused_stream",
	"refused stream",
	"enhance_your_calm",
	"goaway",
	"go away",
	"sendrequest",
}
