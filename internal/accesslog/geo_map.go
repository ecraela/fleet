package accesslog

import (
	"bytes"
	"errors"
	"fmt"
)

func (r *GeoResolver) ClientPolicy() GeoPolicy {
	p := r.policy
	p.TrustedProxies = append([]string{}, p.TrustedProxies...)
	return p
}

// MapSnapshot compiles only public dataset prefixes, never observed clients.
// HAProxy loads this immutable snapshot and performs lookups without the agent.
func (r *GeoResolver) MapSnapshot(kind string) ([]byte, error) {
	db := r.country
	if kind == "asn" {
		db = r.asn
	} else if kind != "country" {
		return nil, errors.New("unknown dataset")
	}
	if db == nil {
		return nil, errors.New("local dataset unavailable")
	}
	var out bytes.Buffer
	count := 0
	for value := range db.Networks() {
		prefix := value.Prefix()
		if value.Err() != nil || !prefix.IsValid() {
			return nil, errors.New("invalid dataset network")
		}
		var result string
		if kind == "country" {
			var record struct {
				Country struct {
					ISO string `maxminddb:"iso_code,maxsize:2"`
				} `maxminddb:"country"`
			}
			if value.Decode(&record) != nil || record.Country.ISO != "" && !ValidCountry(record.Country.ISO) {
				return nil, errors.New("invalid country dataset")
			}
			result = record.Country.ISO
		} else {
			var record struct {
				ASN uint32 `maxminddb:"autonomous_system_number"`
			}
			if value.Decode(&record) != nil {
				return nil, errors.New("invalid ASN dataset")
			}
			if record.ASN > 0 {
				result = fmt.Sprint(record.ASN)
			}
		}
		if result == "" {
			continue
		}
		count++
		if count > 2000000 || out.Len()+len(result)+len(prefix.String())+2 > 64<<20 {
			return nil, errors.New("compiled dataset exceeds local map bound")
		}
		fmt.Fprintf(&out, "%s %s\n", prefix, result)
	}
	return out.Bytes(), nil
}
