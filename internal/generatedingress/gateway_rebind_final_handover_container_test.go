package generatedingress

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestGatewayRebindFinalHandoverContainerAcceptsExactStoppedRunningAndCreateReadback(t *testing.T) {
	value, observed := newGatewayRebindFinalHandoverPlanTestContext(t)
	plan, err := gatewayRebindFinalHandoverPlanFor(value, observed)
	if err != nil {
		t.Fatal(err)
	}
	value.Plan = &plan
	id := strings.Repeat("9", 64)
	binding, err := gatewayRebindFinalContainerBindingFor(value, id)
	if err != nil {
		t.Fatal(err)
	}
	value.Final = &binding
	for _, running := range []bool{false, true} {
		container, runtime := gatewayRebindFinalHandoverTestContainer(value, id, running)
		before, _ := json.Marshal(value)
		if !validGatewayRebindFinalHandoverContainer(value, container, runtime, id) {
			t.Fatalf("exact container rejected (running=%v)", running)
		}
		container.Mounts[0], container.Mounts[1] = container.Mounts[1], container.Mounts[0]
		container.Labels["org.opencontainers.image.title"] = "pinned image metadata"
		runtime.EffectivePortBindings["443/tcp"] = nil
		if !validGatewayRebindFinalHandoverContainer(value, container, runtime, id) {
			t.Fatalf("valid Docker representation rejected (running=%v)", running)
		}
		after, _ := json.Marshal(value)
		if string(before) != string(after) {
			t.Fatal("container validation changed the immutable source or successor plan")
		}
	}
	container, runtime := gatewayRebindFinalHandoverTestContainer(value, id, false)
	for name, configured := range runtime.ConfiguredNetworks {
		configured.NetworkID = ""
		runtime.ConfiguredNetworks[name] = configured
	}
	container.Networks = nil
	if !validGatewayRebindFinalHandoverContainer(value, container, runtime, id) {
		t.Fatal("stopped Docker attachment omission rejected")
	}
	value.Final, value.CreatedFinalID = nil, id
	if !validGatewayRebindFinalHandoverContainer(value, container, runtime, id) {
		t.Fatal("successful-create exact stopped readback rejected")
	}
}

func TestGatewayRebindFinalHandoverContainerRejectsIdentityHardeningPublicationAndNetworkDrift(t *testing.T) {
	base, observed := newGatewayRebindFinalHandoverPlanTestContext(t)
	plan, err := gatewayRebindFinalHandoverPlanFor(base, observed)
	if err != nil {
		t.Fatal(err)
	}
	base.Plan = &plan
	id := strings.Repeat("9", 64)
	binding, err := gatewayRebindFinalContainerBindingFor(base, id)
	if err != nil {
		t.Fatal(err)
	}
	base.Final = &binding
	if len(plan.ApplicationNetworks) == 0 {
		t.Fatal("fixture must include a pinned application network")
	}
	appNetwork := plan.ApplicationNetworks[0].Name
	ingress := base.Intent.Intent.Identity.IngressNetwork
	lanPort := strconv.FormatUint(uint64(base.Intent.Intent.SuccessorProfile.PortStart), 10) + "/tcp"
	type mutation func(*gatewayRebindFinalHandoverContext, *caddyInspection, *gatewayContainerRuntime)
	tests := map[string]mutation{
		"missing plan": func(v *gatewayRebindFinalHandoverContext, _ *caddyInspection, _ *gatewayContainerRuntime) {
			v.Plan = nil
		},
		"forged plan": func(v *gatewayRebindFinalHandoverContext, _ *caddyInspection, _ *gatewayContainerRuntime) {
			v.Plan.LocalHostPort++
		},
		"unbound named container": func(v *gatewayRebindFinalHandoverContext, _ *caddyInspection, _ *gatewayContainerRuntime) {
			v.Final = nil
		},
		"wrong binding": func(v *gatewayRebindFinalHandoverContext, _ *caddyInspection, _ *gatewayContainerRuntime) {
			v.Final.ID = strings.Repeat("8", 64)
		},
		"forged ownership": func(v *gatewayRebindFinalHandoverContext, _ *caddyInspection, _ *gatewayContainerRuntime) {
			v.Final.OwnershipDigest = strings.Repeat("8", 64)
		},
		"forged configuration": func(v *gatewayRebindFinalHandoverContext, _ *caddyInspection, _ *gatewayContainerRuntime) {
			v.Final.ConfigurationDigest = strings.Repeat("8", 64)
		},
		"mixed provisional authority": func(v *gatewayRebindFinalHandoverContext, _ *caddyInspection, _ *gatewayContainerRuntime) {
			v.CreatedFinalID = id
		},
		"wrong ID": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.ID = strings.Repeat("8", 64)
		},
		"wrong image": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Image = strings.Repeat("8", 64)
		},
		"old fixed name": func(v *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Name = v.Predecessor.State.Identity.FinalContainer
		},
		"stage hostname": func(v *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Hostname = v.Intent.Intent.Identity.StageHostname
		},
		"root user": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.User = "0:0"
		},
		"wrong network mode": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.NetworkMode = "host"
		},
		"wrong environment": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Env = append(c.Env, "XDG_CONFIG_HOME=/other")
		},
		"writable root": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.ReadOnly = false
		},
		"privileged": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Privileged = true
		},
		"extra capability": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.CapAdd = append(c.CapAdd, "SYS_ADMIN")
		},
		"missing cap drop": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.CapDrop = nil
		},
		"privilege escalation": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.SecurityOpt = nil
		},
		"host bind": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Binds = []string{"/:/host"}
		},
		"tmpfs": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Tmpfs = map[string]string{"/config": "rw"}
		},
		"memory": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) { c.Memory++ },
		"swap": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.MemorySwap++
		},
		"CPU": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.NanoCPUs++
		},
		"PIDs": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.PIDsLimit++
		},
		"log driver": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.LogType = "json-file"
		},
		"log limit": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.LogConfig["max-size"] = "100m"
		},
		"extra log option": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.LogConfig["extra"] = "value"
		},
		"restart policy": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Restart = "always"
		},
		"entrypoint": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Entrypoint = []string{"sh"}
		},
		"stage config": func(v *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Cmd[2] = "/config/" + v.Intent.Intent.Identity.StageConfigFilename
		},
		"extra argument": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Cmd = append(c.Cmd, "--watch")
		},
		"ulimit": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Ulimits[0].Hard++
		},
		"missing ownership": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			delete(c.Labels, gatewayV2ManagedLabelKey)
		},
		"extra Rig label": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Labels["io.rig.extra"] = "foreign"
		},
		"wrong volume": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Mounts[0].Name = "foreign"
		},
		"duplicate volume": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Mounts[1] = c.Mounts[0]
		},
		"readonly config": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Mounts[0].RW = false
		},
		"extra mount": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Mounts = append(c.Mounts, mountInspection{})
		},
		"missing LAN port": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			delete(c.PortBindings, lanPort)
		},
		"wildcard LAN bind": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.PortBindings[lanPort][0]["HostIp"] = "0.0.0.0"
		},
		"stale selected bind": func(v *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.PortBindings[lanPort][0]["HostIp"] = v.Predecessor.State.Profile.SelectedIPv4
		},
		"wrong LAN port": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.PortBindings[lanPort][0]["HostPort"] = "1"
		},
		"extra LAN binding": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.PortBindings[lanPort] = append(c.PortBindings[lanPort], c.PortBindings[lanPort][0])
		},
		"missing loopback": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			delete(c.PortBindings, "8080/tcp")
		},
		"public loopback": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.PortBindings["8080/tcp"][0]["HostIp"] = "0.0.0.0"
		},
		"new loopback port": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.PortBindings["8080/tcp"][0]["HostPort"] = "1"
		},
		"extra publication": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.PortBindings["80/tcp"] = []map[string]string{{"HostIp": "0.0.0.0", "HostPort": "80"}}
		},
		"restarting": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Restarting = true
		},
		"paused": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			r.Paused = true
		},
		"dead": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			r.Dead = true
		},
		"restarted": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			r.RestartCount = 1
		},
		"negative restart count": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			r.RestartCount = -1
		},
		"missing configured app network": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			delete(r.ConfiguredNetworks, appNetwork)
		},
		"extra configured network": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			r.ConfiguredNetworks["foreign"] = gatewayV2ConfiguredNetwork{}
		},
		"changed configured app network ID": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			n := r.ConfiguredNetworks[appNetwork]
			n.NetworkID = strings.Repeat("8", 64)
			r.ConfiguredNetworks[appNetwork] = n
		},
		"changed configured ingress network ID": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			n := r.ConfiguredNetworks[ingress]
			n.NetworkID = strings.Repeat("8", 64)
			r.ConfiguredNetworks[ingress] = n
		},
		"app gateway priority": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			n := r.ConfiguredNetworks[appNetwork]
			n.GwPriority = 1
			r.ConfiguredNetworks[appNetwork] = n
		},
		"ingress gateway priority": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			n := r.ConfiguredNetworks[ingress]
			n.GwPriority = 0
			r.ConfiguredNetworks[ingress] = n
		},
		"missing static IPAM": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			n := r.ConfiguredNetworks[ingress]
			n.IPAMConfig = nil
			r.ConfiguredNetworks[ingress] = n
		},
		"wrong static IPAM": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			r.ConfiguredNetworks[ingress].IPAMConfig.IPv4Address = "10.0.0.99"
		},
		"IPv6 IPAM": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			r.ConfiguredNetworks[ingress].IPAMConfig.IPv6Address = "::1"
		},
		"static app IPAM": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			n := r.ConfiguredNetworks[appNetwork]
			n.IPAMConfig = &gatewayV2ConfiguredIPAM{IPv4Address: "10.0.0.8"}
			r.ConfiguredNetworks[appNetwork] = n
		},
		"configured IPv6 gateway": func(_ *gatewayRebindFinalHandoverContext, _ *caddyInspection, r *gatewayContainerRuntime) {
			n := r.ConfiguredNetworks[appNetwork]
			n.IPv6Gateway = "::1"
			r.ConfiguredNetworks[appNetwork] = n
		},
		"missing inspected attachment": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			delete(c.Networks, appNetwork)
		},
		"wrong inspected attachment IP": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Networks[appNetwork].IPAddress = "10.0.0.99"
		},
		"inspected IPv6 gateway": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Networks[appNetwork].IPv6Gateway = "::1"
		},
		"inspected app gateway priority": func(_ *gatewayRebindFinalHandoverContext, c *caddyInspection, _ *gatewayContainerRuntime) {
			c.Networks[appNetwork].GwPriority = 1
		},
	}
	for _, running := range []bool{false, true} {
		for name, mutate := range tests {
			t.Run(strconv.FormatBool(running)+"/"+name, func(t *testing.T) {
				value := base
				planCopy, bindingCopy := plan, binding
				value.Plan, value.Final = &planCopy, &bindingCopy
				container, runtime := gatewayRebindFinalHandoverTestContainer(value, id, running)
				mutate(&value, &container, &runtime)
				if validGatewayRebindFinalHandoverContainer(value, container, runtime, id) {
					t.Fatal("drift accepted")
				}
			})
		}
	}
	for _, running := range []bool{false, true} {
		t.Run(strconv.FormatBool(running)+"/runtime-boundary", func(t *testing.T) {
			container, runtime := gatewayRebindFinalHandoverTestContainer(base, id, running)
			for _, expectedID := range []string{"", "short", "sha256:" + id, base.SequenceTwelve.Stage.StageContainer.ID, base.Predecessor.Journal.Resources.FinalContainerID} {
				if validGatewayRebindFinalHandoverContainer(base, container, runtime, expectedID) {
					t.Fatal("invalid expected identity accepted")
				}
			}
			if running {
				for _, corrupt := range []func(*gatewayContainerRuntime){
					func(r *gatewayContainerRuntime) { delete(r.EffectivePortBindings, "8080/tcp") },
					func(r *gatewayContainerRuntime) {
						n := r.ConfiguredNetworks[appNetwork]
						n.NetworkID = ""
						r.ConfiguredNetworks[appNetwork] = n
					},
					func(r *gatewayContainerRuntime) {
						n := r.ConfiguredNetworks[appNetwork]
						n.EndpointID = ""
						r.ConfiguredNetworks[appNetwork] = n
					},
					func(r *gatewayContainerRuntime) {
						n := r.ConfiguredNetworks[ingress]
						n.EndpointID = "sha256:" + n.EndpointID
						r.ConfiguredNetworks[ingress] = n
					},
					func(r *gatewayContainerRuntime) {
						n := r.ConfiguredNetworks[appNetwork]
						n.IPAddress = "::1"
						r.ConfiguredNetworks[appNetwork] = n
					},
					func(r *gatewayContainerRuntime) {
						n := r.ConfiguredNetworks[appNetwork]
						n.IPAddress = "0.0.0.0"
						r.ConfiguredNetworks[appNetwork] = n
					},
				} {
					candidate, physical := gatewayRebindFinalHandoverTestContainer(base, id, true)
					corrupt(&physical)
					if validGatewayRebindFinalHandoverContainer(base, candidate, physical, id) {
						t.Fatal("invalid running proof accepted")
					}
				}
				value := base
				value.Final, value.CreatedFinalID = nil, id
				if validGatewayRebindFinalHandoverContainer(value, container, runtime, id) {
					t.Fatal("provisional identity authorized running final")
				}
			} else {
				for _, corrupt := range []func(*gatewayContainerRuntime){
					func(r *gatewayContainerRuntime) {
						r.EffectivePortBindings["8080/tcp"] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "7346"}}
					},
					func(r *gatewayContainerRuntime) {
						n := r.ConfiguredNetworks[appNetwork]
						n.EndpointID = strings.Repeat("a", 64)
						r.ConfiguredNetworks[appNetwork] = n
					},
					func(r *gatewayContainerRuntime) {
						n := r.ConfiguredNetworks[ingress]
						n.IPAddress = base.Intent.Intent.Network.ContainerIPv4
						r.ConfiguredNetworks[ingress] = n
					},
				} {
					candidate, physical := gatewayRebindFinalHandoverTestContainer(base, id, false)
					corrupt(&physical)
					if validGatewayRebindFinalHandoverContainer(base, candidate, physical, id) {
						t.Fatal("live residue accepted as stopped")
					}
				}
			}
		})
	}
}

func gatewayRebindFinalHandoverTestContainer(value gatewayRebindFinalHandoverContext, id string, running bool) (caddyInspection, gatewayContainerRuntime) {
	identity := value.Intent.Intent.Identity
	container := caddyInspection{
		ID: id, Name: "/" + identity.FinalContainer, Image: "sha256:" + value.SequenceTwelve.Stage.ObservedDockerImageID,
		Labels:   gatewayRebindStageResourceLabels(value.Intent, gatewayV2ManagedContainerLabel, gatewayV2FinalContainerRole),
		Hostname: identity.FinalHostname, User: "1000:1000", Env: []string{"XDG_CONFIG_HOME=/config", "XDG_DATA_HOME=/data"},
		Entrypoint: []string{caddyExecutable}, Cmd: []string{"run", "--config", "/config/" + identity.ActiveConfigFilename},
		ReadOnly: true, CapAdd: []string{caddyCapability}, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
		Mounts: []mountInspection{{Type: "volume", Name: identity.ConfigVolume, Destination: "/config", RW: true}, {Type: "volume", Name: identity.DataVolume, Destination: "/data", RW: true}},
		Memory: 268435456, MemorySwap: 268435456, NanoCPUs: 1_000_000_000, PIDsLimit: 128,
		LogType: "local", LogConfig: map[string]string{"max-size": "10m", "max-file": "3"}, Restart: gatewayV2FinalRestartPolicy,
		NetworkMode: identity.IngressNetwork, Ulimits: []ulimitInspection{{Name: "nofile", Hard: 1024, Soft: 1024}}, Running: running,
		PortBindings: make(map[string][]map[string]string), Networks: make(map[string]*networkAttachment),
	}
	profile := value.Intent.Intent.SuccessorProfile
	for port := profile.PortStart; ; port++ {
		text := strconv.FormatUint(uint64(port), 10)
		container.PortBindings[text+"/tcp"] = []map[string]string{{"HostIp": profile.SelectedIPv4, "HostPort": text}}
		if port == profile.PortEnd {
			break
		}
	}
	container.PortBindings["8080/tcp"] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.FormatUint(uint64(value.Plan.LocalHostPort), 10)}}
	runtime := gatewayContainerRuntime{EffectivePortBindings: make(map[string][]map[string]string), ConfiguredNetworks: make(map[string]gatewayV2ConfiguredNetwork)}
	networks := append([]gatewayRebindHandoverApplicationNetwork{{Name: identity.IngressNetwork, ID: value.SequenceTwelve.Stage.Network.ID}}, value.Plan.ApplicationNetworks...)
	for index, network := range networks {
		configured := gatewayV2ConfiguredNetwork{NetworkID: network.ID}
		if index == 0 {
			configured.GwPriority = caddyGatewayPriority
			configured.IPAMConfig = &gatewayV2ConfiguredIPAM{IPv4Address: value.Intent.Intent.Network.ContainerIPv4}
		}
		if running {
			configured.EndpointID = strings.Repeat("a", 60) + "000" + strconv.Itoa(index)
			configured.IPAddress = "10.44.0." + strconv.Itoa(index+2)
			if index == 0 {
				configured.IPAddress = value.Intent.Intent.Network.ContainerIPv4
			}
		}
		runtime.ConfiguredNetworks[network.Name] = configured
		container.Networks[network.Name] = &networkAttachment{IPAddress: configured.IPAddress, GwPriority: configured.GwPriority}
	}
	if running {
		encoded, _ := json.Marshal(container.PortBindings)
		_ = json.Unmarshal(encoded, &runtime.EffectivePortBindings)
		if !reflect.DeepEqual(runtime.EffectivePortBindings, container.PortBindings) {
			panic("invalid test publication fixture")
		}
	}
	return container, runtime
}
