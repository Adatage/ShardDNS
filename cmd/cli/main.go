// Command sharddns-cli is a small stdlib-only administration client for
// ShardDNS. It talks to the gRPC admin API and prints results as text
// tables via text/tabwriter.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	dnsmgr "github.com/Adatage/ShardDNS/api"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const usage = `sharddns-cli — ShardDNS admin client

Usage:
  sharddns-cli [--addr HOST:PORT] <command> [args]

Global flags:
  --addr   gRPC server address (default localhost:9053)

Commands:
  zone create <name> [--ns NS] [--email EMAIL] [--refresh N] [--retry N] [--expire N] [--ttl N]
  zone list
  zone get <name>
  zone delete <name>
  record add <zone> <name> <type> <ttl> <rdata>
  record list <zone>
  record get <zone> <name> <type>
  record delete <zone> <name> <type> <rdata>
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	// Manual pre-parse: consume --addr / -addr if present anywhere before
	// the subcommand so the CLI feels natural regardless of order.
	addr := "localhost:9053"
	args := make([]string, 0, len(os.Args)-1)
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		switch {
		case a == "--addr" || a == "-addr":
			if i+1 >= len(os.Args) {
				die("--addr requires a value")
			}
			addr = os.Args[i+1]
			i++
		case a == "--help" || a == "-h" || a == "help":
			fmt.Print(usage)
			return
		default:
			args = append(args, a)
		}
	}
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		die("dial %s: %v", addr, err)
	}
	defer conn.Close()

	client := dnsmgr.NewDNSManagerClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch args[0] {
	case "zone":
		runZone(ctx, client, args[1:])
	case "record":
		runRecord(ctx, client, args[1:])
	default:
		die("unknown command %q\n%s", args[0], usage)
	}
}

// --------------------------------------------------------------------------
// zone subcommands
// --------------------------------------------------------------------------

func runZone(ctx context.Context, c dnsmgr.DNSManagerClient, args []string) {
	if len(args) == 0 {
		die("zone: subcommand required (create|list|get|delete)")
	}
	switch args[0] {
	case "create":
		zoneCreate(ctx, c, args[1:])
	case "list":
		zoneList(ctx, c)
	case "get":
		if len(args) < 2 {
			die("zone get: name required")
		}
		zoneGet(ctx, c, args[1])
	case "delete", "del", "rm":
		if len(args) < 2 {
			die("zone delete: name required")
		}
		zoneDelete(ctx, c, args[1])
	default:
		die("zone: unknown subcommand %q", args[0])
	}
}

func zoneCreate(ctx context.Context, c dnsmgr.DNSManagerClient, args []string) {
	fs := flag.NewFlagSet("zone create", flag.ExitOnError)
	ns := fs.String("ns", "", "primary name server (default ns1.<zone>.)")
	email := fs.String("email", "", "admin email (default admin.<zone>.)")
	refresh := fs.Int("refresh", 3600, "SOA refresh in seconds")
	retry := fs.Int("retry", 900, "SOA retry in seconds")
	expire := fs.Int("expire", 604800, "SOA expire in seconds")
	ttl := fs.Int("ttl", 300, "SOA minimum TTL in seconds")

	// The first positional is the zone name.
	if len(args) == 0 {
		die("zone create: name required")
	}
	name := args[0]
	_ = fs.Parse(args[1:])

	req := &dnsmgr.CreateZoneRequest{
		Name:       name,
		PrimaryNs:  *ns,
		AdminEmail: *email,
		Refresh:    int32(*refresh),
		Retry:      int32(*retry),
		Expire:     int32(*expire),
		MinimumTtl: int32(*ttl),
	}
	z, err := c.CreateZone(ctx, req)
	if err != nil {
		die("create zone: %v", err)
	}
	printZones([]*dnsmgr.Zone{z})
}

func zoneList(ctx context.Context, c dnsmgr.DNSManagerClient) {
	resp, err := c.ListZones(ctx, &dnsmgr.ListZonesRequest{PageSize: 500})
	if err != nil {
		die("list zones: %v", err)
	}
	printZones(resp.GetZones())
}

func zoneGet(ctx context.Context, c dnsmgr.DNSManagerClient, name string) {
	z, err := c.GetZone(ctx, &dnsmgr.GetZoneRequest{Name: name})
	if err != nil {
		die("get zone: %v", err)
	}
	printZones([]*dnsmgr.Zone{z})
}

func zoneDelete(ctx context.Context, c dnsmgr.DNSManagerClient, name string) {
	_, err := c.DeleteZone(ctx, &dnsmgr.DeleteZoneRequest{Name: name})
	if err != nil {
		die("delete zone: %v", err)
	}
	fmt.Printf("deleted zone %q\n", name)
}

// --------------------------------------------------------------------------
// record subcommands
// --------------------------------------------------------------------------

func runRecord(ctx context.Context, c dnsmgr.DNSManagerClient, args []string) {
	if len(args) == 0 {
		die("record: subcommand required (add|list|get|delete)")
	}
	switch args[0] {
	case "add", "create":
		if len(args) < 6 {
			die("record add: usage <zone> <name> <type> <ttl> <rdata...>")
		}
		recordAdd(ctx, c, args[1], args[2], args[3], args[4], joinFrom(args, 5))
	case "list":
		if len(args) < 2 {
			die("record list: zone required")
		}
		recordList(ctx, c, args[1])
	case "get":
		if len(args) < 4 {
			die("record get: usage <zone> <name> <type>")
		}
		recordGet(ctx, c, args[1], args[2], args[3])
	case "delete", "del", "rm":
		if len(args) < 5 {
			die("record delete: usage <zone> <name> <type> <rdata...>")
		}
		recordDelete(ctx, c, args[1], args[2], args[3], joinFrom(args, 4))
	default:
		die("record: unknown subcommand %q", args[0])
	}
}

func recordAdd(ctx context.Context, c dnsmgr.DNSManagerClient, zone, name, rtype, ttlStr, rdata string) {
	ttl, err := strconv.Atoi(ttlStr)
	if err != nil {
		die("ttl: %v", err)
	}
	req := &dnsmgr.CreateRecordRequest{
		Zone:  zone,
		Name:  name,
		Type:  rtype,
		Ttl:   int32(ttl),
		Rdata: rdata,
	}
	r, err := c.CreateRecord(ctx, req)
	if err != nil {
		die("create record: %v", err)
	}
	printRecords([]*dnsmgr.Record{r})
}

func recordList(ctx context.Context, c dnsmgr.DNSManagerClient, zone string) {
	resp, err := c.ListRecords(ctx, &dnsmgr.ListRecordsRequest{Zone: zone, PageSize: 500})
	if err != nil {
		die("list records: %v", err)
	}
	printRecords(resp.GetRecords())
}

func recordGet(ctx context.Context, c dnsmgr.DNSManagerClient, zone, name, rtype string) {
	resp, err := c.GetRecords(ctx, &dnsmgr.GetRecordsRequest{Zone: zone, Name: name, Type: rtype})
	if err != nil {
		die("get records: %v", err)
	}
	printRecords(resp.GetRecords())
}

func recordDelete(ctx context.Context, c dnsmgr.DNSManagerClient, zone, name, rtype, rdata string) {
	_, err := c.DeleteRecord(ctx, &dnsmgr.DeleteRecordRequest{
		Zone: zone, Name: name, Type: rtype, Rdata: rdata,
	})
	if err != nil {
		die("delete record: %v", err)
	}
	fmt.Printf("deleted record %s/%s %s %q\n", zone, name, rtype, rdata)
}

// --------------------------------------------------------------------------
// Output helpers
// --------------------------------------------------------------------------

func printZones(zones []*dnsmgr.Zone) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPRIMARY_NS\tADMIN_EMAIL\tSERIAL\tREFRESH\tRETRY\tEXPIRE\tMIN_TTL\tUPDATED_AT")
	for _, z := range zones {
		updated := ""
		if ts := z.GetUpdatedAt(); ts != nil {
			updated = ts.AsTime().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n",
			z.GetName(), z.GetPrimaryNs(), z.GetAdminEmail(),
			z.GetSerial(), z.GetRefresh(), z.GetRetry(), z.GetExpire(),
			z.GetMinimumTtl(), updated)
	}
	_ = w.Flush()
}

func printRecords(records []*dnsmgr.Record) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ZONE\tNAME\tTYPE\tTTL\tRDATA")
	for _, r := range records {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n",
			r.GetZone(), r.GetName(), r.GetType(), r.GetTtl(), r.GetRdata())
	}
	_ = w.Flush()
}

func joinFrom(args []string, i int) string {
	s := ""
	for j := i; j < len(args); j++ {
		if j > i {
			s += " "
		}
		s += args[j]
	}
	return s
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
