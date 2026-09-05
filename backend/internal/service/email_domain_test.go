package service

import (
	"strings"
	"testing"
)

func TestNormalizeEmailSettingDefaultsToMainstreamRegistrationDomains(t *testing.T) {
	setting := normalizeEmailSetting(emailSettingValue{})
	for _, domain := range []string{"gmail.com", "163.com", "126.com", "qq.com", "outlook.com", "hotmail.com", "icloud.com", "yahoo.com", "foxmail.com"} {
		if !containsEmailDomain(setting.RegistrationAllowedDomains, domain) {
			t.Fatalf("default allowed domains = %#v, missing %q", setting.RegistrationAllowedDomains, domain)
		}
	}

	setting = normalizeEmailSetting(emailSettingValue{RegistrationAllowedDomains: []string{}})
	if len(setting.RegistrationAllowedDomains) != 0 {
		t.Fatalf("explicit empty allowed domains = %#v, want unrestricted", setting.RegistrationAllowedDomains)
	}
}

func TestValidateRegistrationEmailDomainUsesExactWhitelistMatch(t *testing.T) {
	allowed := []string{"gmail.com", "example.org"}
	for _, email := range []string{"member@gmail.com", "member@example.org"} {
		if err := validateRegistrationEmailDomain(email, allowed); err != nil {
			t.Fatalf("%s should be allowed: %v", email, err)
		}
	}
	for _, test := range []struct {
		email string
		want  string
	}{
		{email: "member@fakegmail.com", want: "不在管理员设置的白名单"},
		{email: "member@blocked.example", want: "不在管理员设置的白名单"},
	} {
		if err := validateRegistrationEmailDomain(test.email, allowed); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("validateRegistrationEmailDomain(%q) error = %v", test.email, err)
		}
	}
}

func TestValidateRegistrationEmailDomainsRejectsInvalidDomain(t *testing.T) {
	if err := validateRegistrationEmailDomains([]string{"invalid_domain"}); err == nil {
		t.Fatal("invalid domain should be rejected")
	}
}

func TestValidateRegistrationEmailDomainAllowsAnyDomainWhenWhitelistIsEmpty(t *testing.T) {
	if err := validateRegistrationEmailDomain("member@example.org", nil); err != nil {
		t.Fatalf("empty whitelist should not restrict registration: %v", err)
	}
}

func containsEmailDomain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
