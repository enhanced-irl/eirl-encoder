package modem

import (
	"encoding/xml"
	"io"
	"os"
	"strings"
	"sync"
)

// The automatic APN (D-M7): the operator's, from the mobile-broadband-provider-info database (the same one GNOME
// and NetworkManager's assistants use), by the network's MCC and MNC. Without the package, or for an operator it does
// not know, "": ModemManager then uses the network's default bearer, which for LTE works with most operators.

// ProvidersFile: the database (the mobile-broadband-provider-info package).
const ProvidersFile = "/usr/share/mobile-broadband-provider-info/serviceproviders.xml"

type providersDB struct {
	Countries []struct {
		Providers []struct {
			Names []string `xml:"name"`
			GSM   struct {
				Networks []struct {
					MCC string `xml:"mcc,attr"`
					MNC string `xml:"mnc,attr"`
				} `xml:"network-id"`
				APNs []struct {
					Value string `xml:"value,attr"`
					Usage []struct {
						Type string `xml:"type,attr"`
					} `xml:"usage"`
				} `xml:"apn"`
			} `xml:"gsm"`
		} `xml:"provider"`
	} `xml:"country"`
}

// netAPN: one provider's internet APN for a network code.
type netAPN struct {
	names []string // lower case
	apn   string
}

var apnCache struct {
	sync.Mutex
	path   string
	loaded bool
	byNet  map[string][]netAPN // "21407" → the providers listed with it
}

// AutoAPN: the internet APN of the operator with that MCC+MNC ("21407"), or "". Several providers share a code
// (virtual operators on a host network): if their APNs differ, the one whose name is the SIM's operator name
// (simName, its SPN) wins, and without a match there is no automatic APN (the network's default, which works for most
// LTE operators, is better than another operator's).
func AutoAPN(path, mccmnc, simName string) string {
	if path == "" {
		path = ProvidersFile
	}
	if len(mccmnc) < 5 || len(mccmnc) > 6 {
		return ""
	}
	c := &apnCache
	c.Lock()
	defer c.Unlock()
	if !c.loaded || c.path != path {
		c.path, c.loaded, c.byNet = path, true, loadProviders(path)
	}
	list := c.byNet[mccmnc]
	if len(list) == 0 {
		// An MNC of two digits written with three ("214007") or the other way round
		if len(mccmnc) == 6 && mccmnc[3] == '0' {
			list = c.byNet[mccmnc[:3]+mccmnc[4:]]
		} else if len(mccmnc) == 5 {
			list = c.byNet[mccmnc[:3]+"0"+mccmnc[3:]]
		}
	}
	return pickAPN(list, simName)
}

func pickAPN(list []netAPN, simName string) string {
	if len(list) == 0 {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(simName))
	if name != "" {
		for _, p := range list {
			for _, n := range p.names {
				if n == name {
					return p.apn
				}
			}
		}
	}
	for _, p := range list[1:] {
		if p.apn != list[0].apn {
			return "" // ambiguous
		}
	}
	return list[0].apn
}

func loadProviders(path string) map[string][]netAPN {
	out := map[string][]netAPN{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	var db providersDB
	if err := xml.NewDecoder(io.LimitReader(f, 8<<20)).Decode(&db); err != nil {
		return out
	}
	for _, country := range db.Countries {
		for _, p := range country.Providers {
			apn := ""
			for _, a := range p.GSM.APNs {
				internet := len(a.Usage) == 0
				for _, u := range a.Usage {
					internet = internet || u.Type == "internet"
				}
				if internet && ValidAPN(a.Value) && a.Value != "" {
					apn = a.Value
					break
				}
			}
			if apn == "" {
				continue
			}
			var names []string
			for _, n := range p.Names {
				if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
					names = append(names, n)
				}
			}
			for _, n := range p.GSM.Networks {
				key := strings.TrimSpace(n.MCC) + strings.TrimSpace(n.MNC)
				if len(key) >= 5 {
					out[key] = append(out[key], netAPN{names: names, apn: apn})
				}
			}
		}
	}
	return out
}
