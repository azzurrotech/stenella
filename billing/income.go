// Package billing computes the super-admin income picture from atp's
// per-client usage billing plus configurable hosting costs. It never invents
// billing logic of its own: revenue comes straight from atp's /billing
// (avg_silo_gb × price_per_gb_hour over the retained window); hosting cost is
// the same storage figure re-priced at the operator's infrastructure rate.
package billing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"azzurrotech/stenella/atpclient"
)

// DefaultHostPricePerGBHour is a sensible default infrastructure rate used
// when the operator has not configured one (USD per GiB per hour).
const DefaultHostPricePerGBHour = 0.01

// HostingConfig is the operator-side cost model.
type HostingConfig struct {
	HostPricePerGBHour float64 `json:"host_price_per_gb_hour"`
	FixedCostPerMonth  float64 `json:"fixed_cost_per_month"`
}

// DefaultHostingConfig returns the shipped defaults.
func DefaultHostingConfig() HostingConfig {
	return HostingConfig{HostPricePerGBHour: DefaultHostPricePerGBHour}
}

// Client is one row of the income report.
type Client struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Chargeable     bool    `json:"chargeable"`
	PricePerGBHour float64 `json:"price_per_gb_hour"`
	AvgSiloGB      float64 `json:"avg_silo_gb"`
	CurrentSiloGB  float64 `json:"current_silo_gb"`
	Hours          int     `json:"hours"`
	Revenue        float64 `json:"revenue"`
	HostCost       float64 `json:"host_cost"`
	Net            float64 `json:"net"`
}

// Report is the whole income picture.
type Report struct {
	Config       HostingConfig `json:"config"`
	WindowHours  int           `json:"window_hours"`
	Clients      []Client      `json:"clients"`
	Revenue      float64       `json:"revenue"`
	HostCost     float64       `json:"host_cost"`
	Net          float64       `json:"net"`
	Projected30d Revenue30     `json:"projected_30d"`
}

// Revenue30 is a 30-day projection based on the current average sizes.
type Revenue30 struct {
	Revenue  float64 `json:"revenue"`
	HostCost float64 `json:"host_cost"`
	Net      float64 `json:"net"`
}

// Calculator combines atp billing data with host cost config.
type Calculator struct {
	root string
	atp  *atpclient.Client

	mu  sync.RWMutex
	cfg HostingConfig
}

// New loads the hosting config (creating defaults if absent).
func New(root string, atp *atpclient.Client) (*Calculator, error) {
	c := &Calculator{root: root, atp: atp, cfg: DefaultHostingConfig()}
	if data, err := os.ReadFile(c.configPath()); err == nil {
		_ = json.Unmarshal(data, &c.cfg)
	}
	return c, nil
}

func (c *Calculator) configPath() string {
	return filepath.Join(c.root, "stenella", "config.json")
}

// Config returns the current hosting config.
func (c *Calculator) Config() HostingConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

// SetConfig persists a new hosting config.
func (c *Calculator) SetConfig(cfg HostingConfig) error {
	if cfg.HostPricePerGBHour < 0 || cfg.FixedCostPerMonth < 0 {
		return errors.New("hosting costs cannot be negative")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.configPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(c.configPath(), data, 0o644); err != nil {
		return err
	}
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
	return nil
}

// Income computes the report from atp's own summary + per-client billing.
func (c *Calculator) Income() (*Report, error) {
	cfg := c.Config()
	summary, err := c.atp.Summary()
	if err != nil {
		return nil, err
	}
	type cli struct {
		ID             string  `json:"id"`
		Name           string  `json:"name"`
		Chargeable     bool    `json:"chargeable"`
		PricePerGBHour float64 `json:"price_per_gb_hour"`
		AvgSiloGB      float64 `json:"avg_silo_gb"`
		TotalCost      float64 `json:"total_cost"`
		SiloGB         float64 `json:"silo_gb"`
	}
	var clients []cli
	if raw, ok := summary["clients"].([]any); ok {
		for _, item := range raw {
			b, err := json.Marshal(item)
			if err != nil {
				continue
			}
			var c cli
			if err := json.Unmarshal(b, &c); err == nil {
				clients = append(clients, c)
			}
		}
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].ID < clients[j].ID })

	rep := &Report{Config: cfg, Clients: []Client{}}
	for _, cl := range clients {
		bill, err := c.atp.Billing(cl.ID)
		var (
			hours     int
			currentGB float64
		)
		revenue := cl.TotalCost
		if err == nil {
			if v, ok := bill["hours"].(float64); ok {
				hours = int(v)
			}
			if v, ok := bill["current_silo_gb"].(float64); ok {
				currentGB = v
			}
		}
		if hours > rep.WindowHours {
			rep.WindowHours = hours
		}
		hostGBHours := cl.AvgSiloGB * float64(hours)
		host := hostGBHours * cfg.HostPricePerGBHour
		if cl.Chargeable {
			rep.Revenue += revenue
			rep.HostCost += host
		}
		row := Client{
			ID: cl.ID, Name: cl.Name, Chargeable: cl.Chargeable, PricePerGBHour: cl.PricePerGBHour,
			AvgSiloGB: cl.AvgSiloGB, CurrentSiloGB: currentGB, Hours: hours,
			Revenue: revenue, HostCost: host, Net: revenue - host,
		}
		rep.Clients = append(rep.Clients, row)
	}
	rep.Net = rep.Revenue - rep.HostCost

	const hpm = 24 * 30 // hours per month
	var rev30, host30 float64
	for _, c := range rep.Clients {
		if !c.Chargeable {
			continue
		}
		rev30 += c.AvgSiloGB * c.PricePerGBHour * hpm
		host30 += c.AvgSiloGB * cfg.HostPricePerGBHour * hpm
	}
	rep.Projected30d = Revenue30{
		Revenue:  rev30,
		HostCost: host30 + cfg.FixedCostPerMonth,
		Net:      rev30 - host30 - cfg.FixedCostPerMonth,
	}
	return rep, nil
}

func (c *Calculator) incomeClient(client string) (map[string]any, error) {
	return c.atp.Billing(client)
}
