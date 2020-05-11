package main

import (
	"fmt"
	"log"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/consul/sdk/testutil/retry"
)

func testAgent(t *testing.T, cb func(*Config)) *Agent {
	logger := log.New(LOGOUT, "", log.LstdFlags)
	conf := DefaultConfig()
	conf.CoordinateUpdateInterval = 200 * time.Millisecond
	if cb != nil {
		cb(conf)
	}

	agent, err := NewAgent(conf, logger)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		if err := agent.Run(); err != nil {
			t.Fatal(err)
		}
	}()

	return agent
}

func TestAgent_registerServiceAndCheck(t *testing.T) {
	t.Parallel()
	s, err := NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	agent := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.Tag = "test"
	})
	defer agent.Shutdown()

	// Lower these retry intervals
	serviceID := fmt.Sprintf("%s:%s", agent.config.Service, agent.id)

	// Make sure the ESM service and TTL check are registered
	ensureRegistered := func(r *retry.R) {
		services, _, err := agent.client.Catalog().Service(agent.config.Service, "", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(services) != 1 {
			r.Fatalf("bad: %v", services)
		}
		if got, want := services[0].ServiceID, serviceID; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := services[0].ServiceName, agent.config.Service; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := services[0].ServiceTags, []string{"test"}; !reflect.DeepEqual(got, want) {
			r.Fatalf("got %q, want %q", got, want)
		}

		checks, _, err := agent.client.Health().Checks(agent.config.Service, nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 1 {
			r.Fatalf("bad: %v", checks)
		}
		if got, want := checks[0].CheckID, fmt.Sprintf("%s:%s:agent-ttl", agent.config.Service, agent.id); got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := checks[0].Name, "Consul External Service Monitor Alive"; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := checks[0].Status, "passing"; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := checks[0].ServiceID, fmt.Sprintf("%s:%s", agent.config.Service, agent.id); got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
		if got, want := checks[0].ServiceName, agent.config.Service; got != want {
			r.Fatalf("got %q, want %q", got, want)
		}
	}
	retry.Run(t, ensureRegistered)

	// Deregister the service
	if err := agent.client.Agent().ServiceDeregister(serviceID); err != nil {
		t.Fatal(err)
	}

	// Make sure the service and check are deregistered
	ensureDeregistered := func(r *retry.R) {
		services, _, err := agent.client.Catalog().Service(agent.config.Service, "", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(services) != 0 {
			r.Fatalf("bad: %v", services[0])
		}

		checks, _, err := agent.client.Health().Checks(agent.config.Service, nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(checks) != 0 {
			r.Fatalf("bad: %v", checks)
		}
	}
	retry.Run(t, ensureDeregistered)

	// Wait for the agent to re-register the service and TTL check
	retry.Run(t, ensureRegistered)

	// Stop the ESM agent
	agent.Shutdown()

	// Make sure the service and check are gone
	retry.Run(t, ensureDeregistered)
}

func TestAgent_uniqueInstanceID(t *testing.T) {
	t.Parallel()

	s, err := NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	// Register first ESM instance
	agent1 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.InstanceID = "unique-instance-id-1"
	})
	defer agent1.Shutdown()

	// Make sure the first ESM service is registered
	retry.Run(t, func(r *retry.R) {
		services, _, err := agent1.client.Catalog().Service(agent1.config.Service, "", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(services) != 1 {
			r.Fatalf("1 service should be registered: %v", services)
		}
		if got, want := services[0].ServiceID, agent1.serviceID(); got != want {
			r.Fatalf("got service id %q, want %q", got, want)
		}
	})

	// Register second ESM instance
	agent2 := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.InstanceID = "unique-instance-id-2"
	})
	defer agent2.Shutdown()

	// Make sure second ESM service is registered
	retry.Run(t, func(r *retry.R) {
		services, _, err := agent2.client.Catalog().Service(agent2.config.Service, "", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(services) != 2 {
			r.Fatalf("2 service should be registered, got: %v", services)
		}
		if got, want := services[1].ServiceID, agent2.serviceID(); got != want {
			r.Fatalf("got service id %q, want %q", got, want)
		}
	})
}

func TestAgent_notUniqueInstanceIDFails(t *testing.T) {
	t.Parallel()
	notUniqueInstanceID := "not-unique-instance-id"

	s, err := NewTestServer()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	// Register first ESM instance
	agent := testAgent(t, func(c *Config) {
		c.HTTPAddr = s.HTTPAddr
		c.InstanceID = notUniqueInstanceID
	})
	defer agent.Shutdown()

	// Make sure the ESM service is registered
	ensureRegistered := func(r *retry.R) {
		services, _, err := agent.client.Catalog().Service(agent.config.Service, "", nil)
		if err != nil {
			r.Fatal(err)
		}
		if len(services) != 1 {
			r.Fatalf("1 service should be registered: %v", services)
		}
		if got, want := services[0].ServiceID, agent.serviceID(); got != want {
			r.Fatalf("got service id %q, want %q", got, want)
		}
	}
	retry.Run(t, ensureRegistered)

	// Create second ESM service with same instance ID
	logger := log.New(LOGOUT, "", log.LstdFlags)
	conf := DefaultConfig()
	conf.InstanceID = notUniqueInstanceID
	conf.HTTPAddr = s.HTTPAddr

	duplicateAgent, err := NewAgent(conf, logger)
	if err != nil {
		t.Fatal(err)
	}

	err = duplicateAgent.Run()
	defer duplicateAgent.Shutdown()

	if err == nil {
		t.Fatal("Failed to error when registering ESM service with same instance ID")
	}

	switch e := err.(type) {
	case *alreadyExistsError:
	default:
		t.Fatalf("Unexpected error type. Wanted an alreadyExistsError type. Error: '%v'", e)
	}
}
