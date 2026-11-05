package accesslog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ecraela/fleet/internal/domain"
	"github.com/oschwald/maxminddb-golang/v2"
)

const MaxForwardedFor = 256

// GeoPolicy is host-owned authority. Browser controls cannot widen proxy trust
// or supply databases. Reader snapshots stay immutable until the agent restarts.
type GeoPolicy struct {
	Version         int      `json:"version"`
	Mode            string   `json:"mode"`
	TrustedProxies  []string `json:"trusted_proxies,omitempty"`
	CountryDatabase string   `json:"country_database,omitempty"`
	ASNDatabase     string   `json:"asn_database,omitempty"`
}

type GeoDatabase struct {
	SHA256  string `json:"sha256"`
	BuiltAt int64  `json:"built_at"`
}
type GeoCollection struct {
	Mode    string       `json:"mode"`
	Profile string       `json:"profile"`
	Status  string       `json:"status"`
	Country *GeoDatabase `json:"country_database,omitempty"`
	ASN     *GeoDatabase `json:"asn_database,omitempty"`
}
type Geography struct {
	Status  string `json:"status"`
	Source  string `json:"source"`
	Country string `json:"country,omitempty"`
	ASN     uint32 `json:"asn,omitempty"`
	Profile string `json:"profile,omitempty"`
}

func ValidCountry(value string) bool {
	return len(value) == 2 && value[0] >= 'A' && value[0] <= 'Z' && value[1] >= 'A' && value[1] <= 'Z'
}
func (g Geography) Valid() bool {
	if !slices.Contains([]string{"peer", "xff", "proxy_protocol", "unknown"}, g.Source) || g.Profile != "" && !domain.ValidDigest(g.Profile) || g.Country != "" && !ValidCountry(g.Country) {
		return false
	}
	switch g.Status {
	case "located":
		return g.Country != "" && g.ASN > 0 && g.Source != "unknown" && g.Profile != ""
	case "partial":
		return (g.Country != "") != (g.ASN > 0) && g.Source != "unknown" && g.Profile != ""
	case "not_found", "non_public", "database_unavailable":
		return g.Country == "" && g.ASN == 0 && g.Source != "unknown" && g.Profile != ""
	case "unresolved":
		return g.Country == "" && g.ASN == 0 && g.Source == "unknown" && g.Profile != ""
	case "disabled", "unavailable":
		return g.Country == "" && g.ASN == 0 && g.Source == "unknown" && g.Profile == ""
	}
	return false
}
func (g GeoCollection) Valid() bool {
	if !slices.Contains([]string{"off", "peer", "xff", "proxy_protocol"}, g.Mode) || !domain.ValidDigest(g.Profile) {
		return false
	}
	if g.Mode == "off" {
		return g.Status == "disabled" && g.Country == nil && g.ASN == nil
	}
	if g.Status != "ready" && g.Status != "database_unavailable" {
		return false
	}
	if g.Status == "ready" && g.Country == nil && g.ASN == nil {
		return false
	}
	for _, db := range []*GeoDatabase{g.Country, g.ASN} {
		if db != nil && (!domain.ValidDigest(db.SHA256) || db.BuiltAt <= 0 || db.BuiltAt > 4102444800) {
			return false
		}
	}
	return true
}

type GeoResolver struct {
	policy       GeoPolicy
	trusted      []netip.Prefix
	country, asn *maxminddb.Reader
	collection   GeoCollection
}

func boundedLocalFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("absolute local file required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > limit {
		return nil, errors.New("bounded owner-writable regular file required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("local file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("local file exceeds its bound")
	}
	return raw, nil
}

func LoadGeoPolicy(path string) (*GeoResolver, error) {
	return loadGeoPolicy(path, true)
}

// LoadClientPolicy shares the host-owned address policy with enforcement. An
// IP-only policy may omit datasets; it is not a ready Geo telemetry collection.
func LoadClientPolicy(path string) (*GeoResolver, error) { return loadGeoPolicy(path, false) }
func loadGeoPolicy(path string, requireDatabase bool) (*GeoResolver, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := boundedLocalFile(path, 16384)
	if err != nil {
		return nil, errors.New("local geography policy unavailable")
	}
	var p GeoPolicy
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || p.Version != 1 || !slices.Contains([]string{"off", "peer", "xff", "proxy_protocol"}, p.Mode) || len(p.TrustedProxies) > 64 {
		return nil, errors.New("invalid local geography policy")
	}
	r := &GeoResolver{policy: p, collection: GeoCollection{Mode: p.Mode, Status: "ready"}}
	seen := map[netip.Prefix]bool{}
	for _, text := range p.TrustedProxies {
		prefix, err := netip.ParsePrefix(text)
		if err != nil || prefix != prefix.Masked() || prefix.Bits() == 0 || prefix.Addr().Is4In6() || seen[prefix] {
			return nil, errors.New("geography proxy ranges must be canonical, explicit and distinct")
		}
		seen[prefix] = true
		r.trusted = append(r.trusted, prefix)
	}
	if p.Mode == "off" {
		if len(p.TrustedProxies) > 0 || p.CountryDatabase != "" || p.ASNDatabase != "" {
			return nil, errors.New("disabled geography cannot declare proxy trust or databases")
		}
		r.collection.Status = "disabled"
	} else if requireDatabase && p.CountryDatabase == "" && p.ASNDatabase == "" || p.Mode == "peer" && len(p.TrustedProxies) > 0 || p.Mode != "peer" && len(p.TrustedProxies) == 0 {
		return nil, errors.New("geography requires local databases and explicit proxy trust for forwarded addresses")
	}
	for _, item := range []struct {
		path     string
		types    []string
		reader   **maxminddb.Reader
		metadata **GeoDatabase
	}{
		{p.CountryDatabase, []string{"GeoIP2-Country", "GeoLite2-Country"}, &r.country, &r.collection.Country},
		{p.ASNDatabase, []string{"GeoLite2-ASN"}, &r.asn, &r.collection.ASN},
	} {
		if item.path == "" {
			continue
		}
		reader, metadata, err := openGeoDatabase(item.path, item.types)
		if err != nil {
			r.collection.Status = "database_unavailable"
			continue
		}
		*item.reader, *item.metadata = reader, metadata
	}
	// Fingerprint the effective policy AND verified database snapshots. Local file
	// paths are neither exported nor sufficient to identify the data being used.
	profile, _ := json.Marshal(struct {
		Policy       GeoPolicy
		Country, ASN *GeoDatabase
	}{p, r.collection.Country, r.collection.ASN})
	hash := sha256.Sum256(profile)
	r.collection.Profile = hex.EncodeToString(hash[:])
	return r, nil
}
func openGeoDatabase(path string, types []string) (*maxminddb.Reader, *GeoDatabase, error) {
	raw, err := boundedLocalFile(path, 32<<20)
	if err != nil {
		return nil, nil, err
	}
	reader, err := maxminddb.OpenBytes(raw)
	if err != nil {
		return nil, nil, err
	}
	if !slices.Contains(types, reader.Metadata.DatabaseType) || reader.Metadata.BuildEpoch == 0 || reader.Metadata.BuildEpoch > 4102444800 {
		reader.Close()
		return nil, nil, errors.New("incompatible geography database")
	}
	if err = reader.Verify(); err != nil {
		reader.Close()
		return nil, nil, errors.New("invalid geography database")
	}
	hash := sha256.Sum256(raw)
	return reader, &GeoDatabase{SHA256: hex.EncodeToString(hash[:]), BuiltAt: int64(reader.Metadata.BuildEpoch)}, nil
}
func (r *GeoResolver) Close() {
	if r == nil {
		return
	}
	for _, db := range []*maxminddb.Reader{r.country, r.asn} {
		if db != nil {
			db.Close()
		}
	}
}
func (r *GeoResolver) Snapshot() *GeoCollection {
	if r == nil {
		return nil
	}
	copy := r.collection
	return &copy
}
func (r *GeoResolver) trusts(ip netip.Addr) bool {
	return slices.ContainsFunc(r.trusted, func(p netip.Prefix) bool { return p.Contains(ip) })
}
func clientAddress(raw string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(raw)
	return ip.Unmap(), err == nil && ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast()
}

// Resolve accepts only the fixed node-local address fields. The transport peer was
// captured in tcp-request connection, before PROXY or HTTP source rewriting.
func (r *GeoResolver) Resolve(peer, accepted string, proxy bool, header string, headerCount int) Geography {
	g := Geography{Status: "disabled", Source: "unknown"}
	if r == nil || r.policy.Mode == "off" {
		return g
	}
	g = Geography{Status: "unresolved", Source: "unknown", Profile: r.collection.Profile}
	ip, ok := clientAddress(peer)
	if !ok {
		return g
	}
	source := "peer"
	switch r.policy.Mode {
	case "proxy_protocol":
		if !proxy || !r.trusts(ip) {
			return g
		}
		ip, ok = clientAddress(accepted)
		if !ok {
			return g
		}
		source = "proxy_protocol"
	case "xff":
		if r.trusts(ip) {
			// Mixed PROXY+XFF chains need a separately defined trust model. Do not
			// guess from the accepted address or choose a convenient leftmost token.
			if proxy || headerCount != 1 || len(header) == 0 || len(header) > MaxForwardedFor {
				return g
			}
			parts := strings.Split(header, ",")
			if len(parts) > 16 {
				return g
			}
			addresses := make([]netip.Addr, len(parts))
			for i, text := range parts {
				address, valid := clientAddress(strings.TrimSpace(text))
				if !valid {
					return g
				}
				addresses[i] = address
			}
			for i := len(addresses) - 1; i >= 0 && r.trusts(ip); i-- {
				ip = addresses[i]
			}
			if r.trusts(ip) {
				return g
			}
			source = "xff"
		}
	}
	g.Source = source
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		g.Status = "non_public"
		return g
	}
	if r.collection.Status != "ready" {
		g.Status = "database_unavailable"
		return g
	}
	if r.country != nil {
		var record struct {
			Country struct {
				ISO string `maxminddb:"iso_code,maxsize:2"`
			} `maxminddb:"country"`
		}
		if r.country.Lookup(ip).Decode(&record) != nil || record.Country.ISO != "" && !ValidCountry(record.Country.ISO) {
			g.Status = "database_unavailable"
			return g
		}
		g.Country = strings.Clone(record.Country.ISO)
	}
	if r.asn != nil {
		var record struct {
			ASN uint32 `maxminddb:"autonomous_system_number"`
		}
		if r.asn.Lookup(ip).Decode(&record) != nil {
			g.Country = ""
			g.Status = "database_unavailable"
			return g
		}
		g.ASN = record.ASN
	}
	switch {
	case g.Country != "" && g.ASN > 0:
		g.Status = "located"
	case g.Country != "" || g.ASN > 0:
		g.Status = "partial"
	default:
		g.Status = "not_found"
	}
	return g
}
