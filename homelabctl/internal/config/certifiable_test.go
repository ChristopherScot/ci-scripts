package config

import "testing"

// Certifiable must answer about the NAME, never about a particular
// domain: a hardcoded allowlist would silently refuse TLS for any domain
// the tool had not been told about.
func TestCertifiableHasNoDomainAllowlist(t *testing.T) {
	for _, h := range []string{
		"argo.home.chrisscotmartin.com", // this cluster's convention
		"approve.chrisscotmartin.com",   // apex, no `home` label
		"example.org",                   // a domain the tool has never seen
		"a.b.co.uk",                     // multi-label public suffix
		"service.customer-domain.io",    // someone else's domain entirely
		"myapp.home",                    // .home is not reserved; only a label here
	} {
		if !Certifiable(h) {
			t.Errorf("Certifiable(%q) = false, want true - a real domain was refused", h)
		}
	}
}

func TestCertifiableRejectsWhatNoCAWillSign(t *testing.T) {
	for _, h := range []string{
		"go",          // single label
		"argo.lab",    // undelegated, this cluster's convention
		"svc.local",   // RFC 6762
		"db.internal", // ICANN private-use
		"x.test",      // RFC 6761
		"y.invalid",   //
		"z.localhost", //
		"w.example",   //
	} {
		if Certifiable(h) {
			t.Errorf("Certifiable(%q) = true, but no CA will issue - cert-manager would retry forever", h)
		}
	}
}
