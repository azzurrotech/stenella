# billing — super-admin income picture for atp usage

## Overview

`billing` computes the **super-admin income report** from two inputs:

1. atp's own per-client usage billing (obtained through
   `atpclient.Client.Summary` and `atpclient.Client.Billing`), and
2. an operator-configured **hosting cost model**.

It deliberately invents no billing logic of its own. Revenue comes straight
from atp's numbers (`avg_silo_gb × price_per_gb_hour` over the retained window);
hosting cost is the same storage figure re-priced at the operator's
infrastructure rate. The package only combines them and projects 30 days.

- Module: `azzurrotech/stenella/billing` (`go.mod`, Go 1.22).
- Imports: standard library plus `azzurrotech/stenella/atpclient`.
- Imported by: `stenella/web` (`admin.go`, and `web.go` to build the
  `Calculator`).

## Public API

### Hosting configuration

```go
const DefaultHostPricePerGBHour = 0.01 // USD per GiB per hour

type HostingConfig struct {
	HostPricePerGBHour float64 `json:"host_price_per_gb_hour"`
	FixedCostPerMonth  float64 `json:"fixed_cost_per_month"`
}

func DefaultHostingConfig() HostingConfig
```

- `DefaultHostingConfig` returns `HostingConfig{HostPricePerGBHour:
  DefaultHostPricePerGBHour}` — i.e. `0.01` and a zero fixed cost.
- `HostingConfig` is the operator-side cost model persisted as JSON.

### Report shapes

```go
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

type Report struct {
	Config       HostingConfig `json:"config"`
	WindowHours  int           `json:"window_hours"`
	Clients      []Client      `json:"clients"`
	Revenue      float64       `json:"revenue"`
	HostCost     float64       `json:"host_cost"`
	Net          float64       `json:"net"`
	Projected30d Revenue30     `json:"projected_30d"`
}

type Revenue30 struct {
	Revenue  float64 `json:"revenue"`
	HostCost float64 `json:"host_cost"`
	Net      float64 `json:"net"`
}
```

- `Client` is one row of the income report. `Net` is always
  `Revenue - HostCost` for that row.
- `Report.WindowHours` is the largest `hours` value seen across clients.
- `Report.Revenue`, `HostCost` and `Net` aggregate **chargeable clients only**
  (see `Income`).
- `Revenue30` is a 30-day projection based on current average sizes.

### Calculator

```go
type Calculator struct { /* unexported fields */ }

func New(root string, atp *atpclient.Client) (*Calculator, error)
func (c *Calculator) Config() HostingConfig
func (c *Calculator) SetConfig(cfg HostingConfig) error
func (c *Calculator) Income() (*Report, error)
```

- `New` starts from `DefaultHostingConfig()` and, if
  `<root>/stenella/config.json` exists, loads it over the defaults. A missing
  file or malformed JSON simply leaves the defaults in place — `New` never
  returns an error for a bad config file.
- `Config` returns a copy of the current config. It is safe for concurrent use
  (`sync.RWMutex`).
- `SetConfig` rejects negative `HostPricePerGBHour` or `FixedCostPerMonth`
  (`"hosting costs cannot be negative"`), creates `<root>/stenella` if needed,
  writes `config.json` (mode `0o644`), and updates the in-memory value.
- `Income` builds the `Report`.

## How `Income` computes the report

1. Read the current `HostingConfig`.
2. Call `atp.Summary()`; decode each element of its `clients` array into an
   internal struct carrying `id`, `name`, `chargeable`, `price_per_gb_hour`,
   `avg_silo_gb`, `total_cost` and `silo_gb`.
3. Sort clients by `ID`.
4. For each client, call `atp.Billing(id)` for `hours` and `current_silo_gb`
   (a billing error leaves both zero but does not abort the report).
5. `revenue = total_cost` (from the summary); `hostGBHours = avg_silo_gb ×
   hours`; `host = hostGBHours × HostPricePerGBHour`.
6. Add `revenue` and `host` to the report totals **only when the client is
   chargeable**; every client still gets a `Client` row.
7. `Report.Net = Report.Revenue − Report.HostCost`.
8. Project 30 days with `hpm = 24 × 30 = 720` hours: revenue and host cost are
   summed over chargeable clients; `FixedCostPerMonth` is added to the projected
   host cost and subtracted from projected net.

## Usage

```go
calc, err := billing.New(root, atp)
if err != nil {
	return err
}
// Persist the operator's cost model.
if err := calc.SetConfig(billing.HostingConfig{
	HostPricePerGBHour: 0.012,
	FixedCostPerMonth:  40,
}); err != nil {
	return err
}
rep, err := calc.Income()
```

## Configuration

| Field | Meaning | Default |
| --- | --- | --- |
| `HostPricePerGBHour` | Operator infrastructure rate, USD per GiB per hour | `0.01` (`DefaultHostPricePerGBHour`) |
| `FixedCostPerMonth` | Fixed monthly overhead added to the 30-day projection | `0` |

- Config file: `<root>/stenella/config.json` (JSON, indent two spaces when
  written).
- Directory mode: `0o755`; file mode: `0o644`.
- Negative costs are refused by `SetConfig`.

## Testing

There are no test files in this package (`go test ./billing/...` reports
`[no test files]`). Its arithmetic is exercised indirectly by the `web`
admin-income handlers and the `TestAdminUsageDashboard` area of the web suite.

From `/home/matthew/Projects/Platform/stenella`:

```sh
go test ./billing/...
go test ./...
```

## Design notes / invariants

- **One revenue source.** Revenue is atp's `total_cost`; this package never
  recomputes a price. Host cost re-prices atp's own `avg_silo_gb` storage
  figure.
- **Chargeable gates the totals, not the rows.** Non-chargeable clients appear
  in `Report.Clients` (so the UI can show them) but contribute nothing to
  `Report.Revenue`, `Report.HostCost`, `Report.Net` or the 30-day projection.
- **Per-row `Net` is explicit.** Each `Client.Net` is set to `Revenue −
  HostCost` regardless of chargeability; only the aggregate filters.
- **Billing lookup is best-effort.** A failing `atp.Billing` call zeroes the
  window/current size for that client instead of failing the whole report.
- **Config reads are forgiving, writes are strict.** Bad or absent JSON falls
  back to defaults; `SetConfig` enforces non-negative values.
