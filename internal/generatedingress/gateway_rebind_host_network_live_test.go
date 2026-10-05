package generatedingress

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/hostd/hostd/internal/hostnetwork"
)

// The caller must opt in explicitly to creating a disposable dummy adapter.
// Its two addresses are selected only from a nonoverlapping private prefix;
// cleanup checks the exact interface identity, alias, kind and owned addresses.
func liveGatewayRebindHostAddresses(t *testing.T, permissionEnvironment string) (hostnetwork.Candidate, hostnetwork.Candidate) {
	t.Helper()
	if os.Getenv("RIG_RUN_LIVE_GATEWAY_V2") != "1" || os.Getenv(permissionEnvironment) != "1" {
		t.Skip("set both live gateway flags on a disposable Linux Docker host")
	}
	if runtime.GOOS != "linux" || os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		t.Fatal("rebind requires the default local Linux Docker host")
	}
	snapshot, err := hostnetwork.CurrentIPv4NetworkSnapshot()
	if err != nil {
		t.Fatal("read complete host routes and interfaces before reserving test prefix")
	}
	var prefix netip.Prefix
	for _, candidate := range []string{"192.168.253.248/30", "172.29.253.248/30", "10.253.253.248/30"} {
		value := netip.MustParsePrefix(candidate)
		if hostnetwork.CheckDockerIngressSubnetNonoverlap(value, snapshot) == nil {
			prefix = value
			break
		}
	}
	if !prefix.IsValid() {
		t.Fatal("no nonoverlapping private prefix available for disposable adapter")
	}
	ip, err := exec.LookPath("ip")
	if err != nil {
		t.Fatal("iproute2 is required for the disposable rebind gate")
	}
	var nonce [5]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("create unique disposable adapter identity")
	}
	name, alias := "righ"+hex.EncodeToString(nonce[:]), "rig-rebind-host-network:"+hex.EncodeToString(nonce[:])
	if _, err := net.InterfaceByName(name); err == nil {
		t.Fatal("test adapter name already exists")
	}
	command := func(ctx context.Context, args ...string) ([]byte, error) {
		var cmd *exec.Cmd
		if os.Geteuid() == 0 {
			cmd = exec.CommandContext(ctx, ip, args...)
		} else {
			sudo, lookupErr := exec.LookPath("sudo")
			if lookupErr != nil {
				return nil, lookupErr
			}
			cmd = exec.CommandContext(ctx, sudo, append([]string{"-n", ip}, args...)...)
		}
		return cmd.Output()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := command(ctx, "link", "add", "name", name, "type", "dummy"); err != nil {
		t.Fatal("create test-owned dummy adapter")
	}
	created, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal("read newly created dummy adapter identity; retaining uncertain adapter")
	}
	first, second := prefix.Addr().Next(), prefix.Addr().Next().Next()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		current, err := net.InterfaceByName(name)
		if err != nil || current.Index != created.Index || current.Name != created.Name {
			t.Error("test adapter identity changed; retaining adapter")
			return
		}
		body, err := command(cleanupCtx, "-json", "-details", "link", "show", "dev", name)
		var links []struct {
			Index int    `json:"ifindex"`
			Name  string `json:"ifname"`
			Alias string `json:"ifalias"`
			Info  struct {
				Kind string `json:"info_kind"`
			} `json:"linkinfo"`
		}
		if err != nil || json.Unmarshal(body, &links) != nil || len(links) != 1 || links[0].Index != created.Index || links[0].Name != name || links[0].Alias != alias || links[0].Info.Kind != "dummy" {
			t.Error("test adapter ownership is uncertain; retaining adapter")
			return
		}
		addresses, err := current.Addrs()
		if err != nil {
			t.Error("cannot enumerate test adapter before cleanup")
			return
		}
		for _, value := range addresses {
			p, err := netip.ParsePrefix(value.String())
			if err != nil || (p.Addr().Is4() && (p.Bits() != 30 || (p.Addr() != first && p.Addr() != second))) ||
				(p.Addr().Is6() && !p.Addr().IsLinkLocalUnicast()) {
				t.Error("test adapter gained an unowned address; retaining adapter")
				return
			}
		}
		if _, err := command(cleanupCtx, "link", "delete", "dev", name); err != nil {
			t.Error("remove exact test-owned dummy adapter")
		}
	})
	if _, err := command(ctx, "link", "set", "dev", name, "alias", alias); err != nil {
		t.Fatal("label test-owned dummy adapter")
	}
	for _, address := range []netip.Addr{first, second} {
		if _, err := command(ctx, "address", "add", address.String()+"/30", "dev", name); err != nil {
			t.Fatal("assign test-owned private address")
		}
	}
	if _, err := command(ctx, "link", "set", "dev", name, "up"); err != nil {
		t.Fatal("activate test-owned dummy adapter")
	}
	candidates, err := hostnetwork.CurrentCandidates()
	if err != nil {
		t.Fatal("enumerate explicit test host candidates")
	}
	identity := fmt.Sprintf("%d/%s", created.Index, name)
	before, err := hostnetwork.Select(candidates, identity, first.String())
	if err != nil {
		t.Fatal("select exact predecessor address")
	}
	after, err := hostnetwork.Select(candidates, identity, second.String())
	if err != nil || before.IPv4 == after.IPv4 {
		t.Fatal("select distinct exact successor address")
	}
	return before, after
}
