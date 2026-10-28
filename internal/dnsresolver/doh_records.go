package dnsresolver

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

func supportedDoHType(recordType int) bool {
	switch recordType {
	case 1, 28, 16, 15, 5, 33, 2, 12:
		return true
	}
	return false
}

func lookupDoH(ctx context.Context, profile Profile, hostname string, recordType int) ([]dohAnswer, error) {
	answer := func(data string) dohAnswer { return dohAnswer{Name: hostname, Type: recordType, TTL: 30, Data: data} }
	if recordType == 1 || recordType == 28 {
		// Preserve literals, including mapped IPv6, before the host lookup adapter
		// normalizes DNS-provided IPv4-mapped addresses for dns.lookup().
		if ip, err := netip.ParseAddr(hostname); err == nil {
			if (recordType == 1) != ip.Is4() {
				return nil, nil
			}
			return []dohAnswer{answer(hostname)}, nil
		}
		family := 4
		if recordType == 28 {
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
	return queryProfile(ctx, profile, hostname, func(ctx context.Context, resolver *net.Resolver) ([]dohAnswer, error) {
		var answers []dohAnswer
		switch recordType {
		case 16:
			records, err := resolver.LookupTXT(ctx, hostname)
			if err != nil {
				return nil, err
			}
			for _, record := range records {
				answers = append(answers, answer(strconv.Quote(record)))
			}
		case 15:
			records, err := resolver.LookupMX(ctx, hostname)
			if err != nil {
				return nil, err
			}
			for _, record := range records {
				answers = append(answers, answer(fmt.Sprintf("%d %s", record.Pref, record.Host)))
			}
		case 5:
			record, err := resolver.LookupCNAME(ctx, hostname)
			if err != nil {
				return nil, err
			}
			if !strings.EqualFold(strings.TrimSuffix(record, "."), strings.TrimSuffix(hostname, ".")) {
				answers = append(answers, answer(record))
			}
		case 33:
			_, records, err := resolver.LookupSRV(ctx, "", "", hostname)
			if err != nil {
				return nil, err
			}
			for _, record := range records {
				// workerd's SRV parser leaves a trailing root dot intact; return
				// the hostname form expected by Node callers and Cloudflare DoH.
				target := record.Target
				if target != "." {
					target = strings.TrimSuffix(target, ".")
				}
				answers = append(answers, answer(fmt.Sprintf("%d %d %d %s", record.Priority, record.Weight, record.Port, target)))
			}
		case 2:
			records, err := resolver.LookupNS(ctx, hostname)
			if err != nil {
				return nil, err
			}
			for _, record := range records {
				answers = append(answers, answer(record.Host))
			}
		case 12:
			address, err := reverseLookupAddress(hostname)
			if err != nil {
				return nil, err
			}
			records, err := resolver.LookupAddr(ctx, address)
			if err != nil {
				return nil, err
			}
			for _, record := range records {
				answers = append(answers, answer(record))
			}
		}
		return answers, nil
	})
}

// workerd reverse() sends an IP; resolvePtr() can send an in-addr.arpa or
// ip6.arpa name. Go LookupAddr expects an IP in both cases.
func reverseLookupAddress(name string) (string, error) {
	if ip, err := netip.ParseAddr(name); err == nil {
		return ip.String(), nil
	}
	lower := strings.TrimSuffix(strings.ToLower(name), ".")
	if prefix, ok := strings.CutSuffix(lower, ".in-addr.arpa"); ok {
		labels := strings.Split(prefix, ".")
		if len(labels) == 4 {
			ip, err := netip.ParseAddr(labels[3] + "." + labels[2] + "." + labels[1] + "." + labels[0])
			if err == nil {
				return ip.String(), nil
			}
		}
	}
	if prefix, ok := strings.CutSuffix(lower, ".ip6.arpa"); ok {
		labels := strings.Split(prefix, ".")
		if len(labels) == 32 {
			var hex strings.Builder
			for i := 31; i >= 0; i-- {
				if len(labels[i]) != 1 || !strings.ContainsAny(labels[i], "0123456789abcdef") {
					return "", &net.DNSError{Err: "invalid reverse lookup name", Name: name, IsNotFound: true}
				}
				hex.WriteString(labels[i])
				if i > 0 && i%4 == 0 {
					hex.WriteByte(':')
				}
			}
			if ip, err := netip.ParseAddr(hex.String()); err == nil {
				return ip.String(), nil
			}
		}
	}
	return "", &net.DNSError{Err: "invalid reverse lookup name", Name: name, IsNotFound: true}
}
