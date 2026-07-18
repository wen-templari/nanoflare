package nanoflare

import (
	"errors"
	"testing"
)

func TestNormalizeUsageLevel(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "", want: UsageLevelDefault},
		{input: "unknown", want: UsageLevelDefault},
		{input: " DEFAULT ", want: UsageLevelDefault},
		{input: " paid ", want: UsageLevelPaid},
	} {
		t.Run(test.input, func(t *testing.T) {
			if got := NormalizeUsageLevel(test.input); got != test.want {
				t.Fatalf("NormalizeUsageLevel(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestOrgLimitsForLevel(t *testing.T) {
	defaults := OrgLimitsForLevel(UsageLevelDefault)
	if defaults.Workers == nil || *defaults.Workers != 3 {
		t.Fatalf("default worker limit = %#v", defaults.Workers)
	}
	if defaults.KVNamespaces == nil || *defaults.KVNamespaces != 3 {
		t.Fatalf("default KV limit = %#v", defaults.KVNamespaces)
	}
	if defaults.ObjectStorageBuckets == nil || *defaults.ObjectStorageBuckets != 3 {
		t.Fatalf("default object bucket limit = %#v", defaults.ObjectStorageBuckets)
	}
	if defaults.OAuthClients == nil || *defaults.OAuthClients != 0 {
		t.Fatalf("default OAuth limit = %#v", defaults.OAuthClients)
	}
	if defaults.ObjectStorageBytes == nil || *defaults.ObjectStorageBytes != 500*1024*1024 {
		t.Fatalf("default object storage byte limit = %#v", defaults.ObjectStorageBytes)
	}
	if defaults.KVStorageBytes == nil || *defaults.KVStorageBytes != 100*1024*1024 {
		t.Fatalf("default KV byte limit = %#v", defaults.KVStorageBytes)
	}

	paid := OrgLimitsForLevel(UsageLevelPaid)
	if paid.Workers != nil || paid.KVNamespaces != nil || paid.ObjectStorageBuckets != nil || paid.OAuthClients != nil || paid.ObjectStorageBytes != nil || paid.KVStorageBytes != nil {
		t.Fatalf("paid limits = %#v, want unlimited nil limits", paid)
	}
}

func TestOAuthClientOrgLimits(t *testing.T) {
	store := NewStore()
	if err := store.CreateOrganization(Organization{ID: "org-default", Name: "Default Org"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOrganization(Organization{ID: "org-paid", Name: "Paid Org", UsageLevel: UsageLevelPaid}); err != nil {
		t.Fatal(err)
	}
	oauth := NewOAuthService(store)
	oauth.hashCost = 4

	input := CreateOAuthClientInput{
		Name:         "External App",
		RedirectURIs: []string{"https://external.example.com/callback"},
		Scopes:       []string{"apps:write"},
	}
	input.OwnerOrgID = "org-default"
	if _, err := oauth.CreateClient(input); !errors.Is(err, ErrUsageLimitExceeded) {
		t.Fatalf("default OAuth client error = %v, want ErrUsageLimitExceeded", err)
	}

	input.OwnerOrgID = "org-paid"
	if _, err := oauth.CreateClient(input); err != nil {
		t.Fatalf("first paid OAuth client: %v", err)
	}
	input.Name = "External App 2"
	if _, err := oauth.CreateClient(input); err != nil {
		t.Fatalf("second paid OAuth client: %v", err)
	}
}
