package dnsresolver

import (
	"context"
	"net/netip"

	"golang.org/x/net/dns/dnsmessage"
)

func supportedDoHType(recordType int) bool {
	for _, kind := range dohRecordTypes {
		if int(kind.code) == recordType {
			return kind.supported
		}
	}
	return false
}

func lookupDoH(ctx context.Context, profile Profile, hostname string, recordType int) ([]dohAnswer, error) {
	answer := func(data string) dohAnswer { return dohAnswer{Name: hostname, Type: recordType, TTL: 30, Data: data} }
	if recordType == int(dnsmessage.TypeA) || recordType == int(dnsmessage.TypeAAAA) {
		// Preserve literals, including mapped IPv6, before the host lookup adapter
		// normalizes DNS-provided IPv4-mapped addresses for dns.lookup().
		if ip, err := netip.ParseAddr(hostname); err == nil {
			if (recordType == int(dnsmessage.TypeA)) != ip.Is4() {
				return nil, nil
			}
			return []dohAnswer{answer(hostname)}, nil
		}
		family := 4
		if recordType == int(dnsmessage.TypeAAAA) {
			family = 6
		}
		addresses, err := lookup(ctx, profile, hostname, family)
		if err != nil {
			return nil, err
		}
		var answers []dohAnswer
		for _, address := range addresses {
			if address.Family == family {
				answers = append(answers, answer(address.Address))
			}
		}
		return answers, nil
	}
	return lookupRecords(ctx, profile, hostname, dnsmessage.Type(recordType))
}
