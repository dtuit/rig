package aws

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// sshPort is the only port rig authorises: ssh from the operator.
const sshPort = 22

// groupDescription is what an auditor reading the VPC's security groups
// sees. EC2 allows no apostrophes here, and the field cannot be edited
// after creation, so it says only what stays true.
const groupDescription = "rig: ssh (22/tcp) to rig-managed instances, from the operator address rig authorised. Created by rig (github.com/dtuit/rig); safe to delete when no rig instances remain."

// groupPurpose is the rig:purpose tag value on that group.
const groupPurpose = "ssh ingress for rig-managed instances"

// checkIPURL reports the caller's public address as one line of text. It is
// AWS's own service: any credential chain that can reach EC2 can reach it,
// so it adds no new dependency and no new vendor.
const checkIPURL = "https://checkip.amazonaws.com"

// publicIPTimeout bounds the lookup — a launch must not hang on it.
const publicIPTimeout = 5 * time.Second

var publicIPClient = &http.Client{Timeout: publicIPTimeout}

// discoverPublicIP asks AWS what address rig is calling from, which is the
// address the operator's ssh will come from too (barring split tunnels,
// where the answer is wrong and providers.aws.ssh_cidrs is the fix).
func discoverPublicIP(ctx context.Context) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, publicIPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkIPURL, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	resp, err := publicIPClient.Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("%s: %s", checkIPURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return netip.Addr{}, err
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%s answered %q, which is not an address", checkIPURL, strings.TrimSpace(string(body)))
	}
	return addr, nil
}

// parsePrefix accepts a CIDR block or a bare address, which means one host.
func parsePrefix(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is neither a CIDR block nor an address", s)
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// sshCIDRs are the blocks the rig group must admit on 22/tcp: what config
// says, or the operator's own address as a /32. A public IP rig cannot
// determine is an error naming the config key — never 0.0.0.0/0, which is
// the failure this replaces.
func (d *Driver) sshCIDRs(ctx context.Context) ([]netip.Prefix, error) {
	if len(d.cfg.SSHCIDRs) > 0 {
		out := make([]netip.Prefix, 0, len(d.cfg.SSHCIDRs))
		for _, s := range d.cfg.SSHCIDRs {
			p, err := parsePrefix(strings.TrimSpace(s))
			if err != nil {
				return nil, fmt.Errorf("aws: providers.aws.ssh_cidrs: %w", err)
			}
			out = append(out, p)
		}
		return out, nil
	}
	addr, err := d.publicIP(ctx)
	if err != nil {
		return nil, fmt.Errorf("aws: cannot work out your public IP to authorise ssh (%v); set providers.aws.ssh_cidrs to the block ssh may come from — [\"1.2.3.4/32\"] for one address, or [\"0.0.0.0/0\"] to accept the whole internet", err)
	}
	return []netip.Prefix{netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen())}, nil
}

// sshPrefixes are the blocks a group already admits on 22/tcp. Rules that
// reference another group or a prefix list are ignored: they may admit the
// operator, but rig cannot tell, and it never revokes anything anyway.
func sshPrefixes(g types.SecurityGroup) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range g.IpPermissions {
		proto := deref(p.IpProtocol)
		if proto != "tcp" && proto != "-1" {
			continue
		}
		if proto == "tcp" && (p.FromPort == nil || p.ToPort == nil || *p.FromPort > sshPort || *p.ToPort < sshPort) {
			continue
		}
		for _, r := range p.IpRanges {
			if pfx, err := netip.ParsePrefix(deref(r.CidrIp)); err == nil {
				out = append(out, pfx.Masked())
			}
		}
		for _, r := range p.Ipv6Ranges {
			if pfx, err := netip.ParsePrefix(deref(r.CidrIpv6)); err == nil {
				out = append(out, pfx.Masked())
			}
		}
	}
	return out
}

// admits reports whether one of have covers the whole of want.
func admits(have []netip.Prefix, want netip.Prefix) bool {
	for _, h := range have {
		if h.Addr().Is4() != want.Addr().Is4() {
			continue
		}
		if h.Bits() <= want.Bits() && h.Contains(want.Addr()) {
			return true
		}
	}
	return false
}

func isWorld(p netip.Prefix) bool { return p.Bits() == 0 }

// sshPermissions is the 22/tcp ingress for cidrs, IPv4 and IPv6 apart as
// EC2 wants them.
func sshPermissions(cidrs []netip.Prefix, description string) []types.IpPermission {
	perm := types.IpPermission{
		IpProtocol: sdk.String("tcp"),
		FromPort:   sdk.Int32(sshPort),
		ToPort:     sdk.Int32(sshPort),
	}
	for _, c := range cidrs {
		if c.Addr().Is4() {
			perm.IpRanges = append(perm.IpRanges, types.IpRange{CidrIp: sdk.String(c.String()), Description: sdk.String(description)})
			continue
		}
		perm.Ipv6Ranges = append(perm.Ipv6Ranges, types.Ipv6Range{CidrIpv6: sdk.String(c.String()), Description: sdk.String(description)})
	}
	return []types.IpPermission{perm}
}

func joinPrefixes(cidrs []netip.Prefix) string {
	parts := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		parts = append(parts, c.String())
	}
	return strings.Join(parts, ", ")
}

// ensureSSHIngress returns the id of the security group launches join,
// creating it in vpc on first use with 22/tcp open to the blocks sshCIDRs
// names and nothing else.
//
// A group rig created (tagged rig:managed=true) whose rules no longer admit
// the operator — the everyday case of a changed home or café address —
// gains the missing block, with a line saying so: telling the user to run
// an AWS CLI command against rig's own group would be friction with no
// safety gained, and the alternative is an instance that only fails at ssh
// time. A group rig did not create is never touched: if it does not admit
// the operator the launch is refused, naming the command that would fix it,
// because widening somebody else's group is not rig's business.
func (d *Driver) ensureSSHIngress(ctx context.Context, vpc string) (string, error) {
	name := d.cfg.SecurityGroup
	groups, err := d.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{filter("vpc-id", vpc), filter("group-name", name)},
	})
	if err != nil {
		return "", d.fail("finding security group "+name, err)
	}
	var existing *types.SecurityGroup
	for i := range groups.SecurityGroups {
		if deref(groups.SecurityGroups[i].GroupName) == name {
			existing = &groups.SecurityGroups[i]
			break
		}
	}

	want, err := d.sshCIDRs(ctx)
	if err != nil {
		return "", err
	}

	if existing == nil {
		created, err := d.ec2.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
			GroupName:   sdk.String(name),
			Description: sdk.String(groupDescription),
			VpcId:       sdk.String(vpc),
			TagSpecifications: []types.TagSpecification{{
				ResourceType: types.ResourceTypeSecurityGroup,
				Tags:         append(managedTags(name), tag(purposeTag, groupPurpose)),
			}},
		})
		if err != nil {
			return "", d.fail("creating security group "+name, err)
		}
		id := deref(created.GroupId)
		if err := d.authorizeSSH(ctx, id, want); err != nil {
			return "", err
		}
		for _, c := range want {
			if isWorld(c) {
				d.logf("warning: security group %s (%s) now allows ssh from the whole internet (%s), because providers.aws.ssh_cidrs asks for it", name, id, c)
			}
		}
		return id, nil
	}

	id := deref(existing.GroupId)
	have := sshPrefixes(*existing)
	var missing []netip.Prefix
	for _, c := range want {
		if !admits(have, c) {
			missing = append(missing, c)
		}
	}
	managed := tagValue(existing.Tags, managedTag) == "true"

	if !managed {
		if len(missing) > 0 {
			return "", fmt.Errorf("aws: security group %s (%s) does not allow ssh from %s, and rig will not change a group it did not create (no %s=true tag). Allow it with: aws ec2 authorize-security-group-ingress --region %s --group-id %s --protocol tcp --port %d --cidr %s%s — or point providers.aws.security_group at a group rig manages",
				name, id, joinPrefixes(missing), managedTag, d.region, id, sshPort, missing[0],
				extraCIDRHint(missing))
		}
		return id, nil
	}

	if len(missing) > 0 {
		if err := d.authorizeSSH(ctx, id, missing); err != nil {
			return "", err
		}
		d.logf("added %s to security group %s (%s) so ssh reaches this instance; rig never removes an address, so revoke stale ones with: aws ec2 revoke-security-group-ingress --region %s --group-id %s --protocol tcp --port %d --cidr <old>",
			joinPrefixes(missing), name, id, d.region, id, sshPort)
		return id, nil
	}

	// Nothing to add — but a group from before rig narrowed its default,
	// or one somebody widened by hand, may still stand open to the world.
	// Saying so is the point: rig will not revoke a rule a colleague may
	// depend on, so the operator decides.
	wantsWorld := false
	for _, c := range want {
		wantsWorld = wantsWorld || isWorld(c)
	}
	if !wantsWorld {
		for _, h := range have {
			if isWorld(h) {
				d.logf("warning: security group %s (%s) allows ssh from the whole internet (%s); rig no longer creates that rule. Remove it with: aws ec2 revoke-security-group-ingress --region %s --group-id %s --protocol tcp --port %d --cidr %s",
					name, id, h, d.region, id, sshPort, h)
			}
		}
	}
	return id, nil
}

// extraCIDRHint mentions the blocks a single --cidr line cannot carry.
func extraCIDRHint(missing []netip.Prefix) string {
	if len(missing) < 2 {
		return ""
	}
	return fmt.Sprintf(" (and once per block for %s)", joinPrefixes(missing[1:]))
}

// authorizeSSH opens 22/tcp from cidrs on the group. A rule EC2 already
// holds is not an error: two rigs launching at once is normal.
func (d *Driver) authorizeSSH(ctx context.Context, group string, cidrs []netip.Prefix) error {
	if len(cidrs) == 0 {
		return nil
	}
	_, err := d.ec2.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId:       sdk.String(group),
		IpPermissions: sshPermissions(cidrs, "rig ssh"),
	})
	if err != nil && errCode(err) != "InvalidPermission.Duplicate" {
		return d.fail(fmt.Sprintf("allowing ssh from %s on security group %s", joinPrefixes(cidrs), d.cfg.SecurityGroup), err)
	}
	return nil
}
