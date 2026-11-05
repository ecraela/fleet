package domain

// This local frontend format deliberately contains no client address, URL,
// headers or body. App identity is the compiler-owned backend, never a header.
// DPA preserves the quoting required by HAProxy's single-argument log-format.
const RequestLogFormat = `"cv1 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts"`

// The operator captures path into txn.cv_path during HTTP request processing.
// Only the local agent sees these bytes; its local policy decides what to send.
const RequestPathLogFormat = `"cv2 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts %[var(txn.cv_path),length] %[var(txn.cv_path),hex]"`

// The operator captures sess.cv_peer in tcp-request connection and txn.cv_xff
// at request receipt. Addresses and forwarded headers are only local datagram
// inputs; the agent exports coarse Geo evidence after its own trust checks.
const LegacyRequestGeoLogFormat = `"cv3 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts %[var(txn.cv_path),length] %[var(txn.cv_path),hex] %[var(sess.cv_peer)] %[fc_src] %[fc_rcvd_proxy] %[var(txn.cv_xff_count)] %[var(txn.cv_xff),length] %[var(txn.cv_xff),hex]"`

// cv4 captures the accepted address after the PROXY handshake, before rewrites.
const RequestGeoLogFormat = `"cv4 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts %[var(txn.cv_path),length] %[var(txn.cv_path),hex] %[var(sess.cv_peer)] %[var(sess.cv_client)] %[fc_rcvd_proxy] %[var(txn.cv_xff_count)] %[var(txn.cv_xff),length] %[var(txn.cv_xff),hex]"`

const RequestTrafficLogFormat = `"cv5 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts %[var(txn.cv_path),length] %[var(txn.cv_path),hex] %[var(sess.cv_peer)] %[var(sess.cv_client)] %[fc_rcvd_proxy] %[var(txn.cv_xff_count)] %[var(txn.cv_xff),length] %[var(txn.cv_xff),hex] %[var(txn.cv_rule)] %[var(txn.cv_decision)]"`

func ValidRequestLogFormat(value string) bool {
	return value == RequestLogFormat || value == RequestPathLogFormat || value == RequestGeoLogFormat || value == LegacyRequestGeoLogFormat || value == RequestTrafficLogFormat
}
