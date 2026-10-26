package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	sharddnsv1 "github.com/Adatage/ShardDNS/api/sharddns/v1"
	"github.com/Adatage/ShardDNS/api/sharddns/v1/sharddnsv1connect"

	"connectrpc.com/connect"
)

var version = "dev"

const usage = `sharddns - ShardDNS admin client

Usage:
  sharddns [--addr URL] [--token TOKEN] <command> [args]

Global flags:
  --addr    API address (default $SHARDDNS_ADDR or http://localhost:9053)
  --token   API bearer token (default $SHARDDNS_TOKEN)

Commands:
  zone create <name> [--ns NS] [--email EMAIL] [--refresh N] [--retry N] [--expire N] [--ttl N]
  zone update <name> [--ns NS] [--email EMAIL] [--refresh N] [--retry N] [--expire N] [--ttl N]
  zone list
  zone get <name>
  zone delete <name>
  record add <zone> <name> <type> <ttl> <rdata>
  record list <zone>
  record get <zone> <name> <type>
  record delete <zone> <name> <type> <rdata>
  version
`

type cli struct {
	client sharddnsv1connect.DNSManagerClient
}

func main() {
	addr := envOr("SHARDDNS_ADDR", "http://localhost:9053")
	token := os.Getenv("SHARDDNS_TOKEN")

	var args []string
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		switch a {
		case "--addr", "-addr":
			i++
			if i >= len(os.Args) {
				die("--addr requires a value")
			}
			addr = os.Args[i]
		case "--token", "-token":
			i++
			if i >= len(os.Args) {
				die("--token requires a value")
			}
			token = os.Args[i]
		case "--help", "-h", "help":
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
	if args[0] == "version" {
		fmt.Println(version)
		return
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}

	var opts []connect.ClientOption
	if token != "" {
		opts = append(opts, connect.WithInterceptors(bearer(token)))
	}
	c := &cli{client: sharddnsv1connect.NewDNSManagerClient(http.DefaultClient, addr, opts...)}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch args[0] {
	case "zone":
		c.zone(ctx, args[1:])
	case "record":
		c.record(ctx, args[1:])
	default:
		die("unknown command %q\n\n%s", args[0], usage)
	}
}

func bearer(token string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	})
}

func (c *cli) zone(ctx context.Context, args []string) {
	if len(args) == 0 {
		die("zone: subcommand required (create|update|list|get|delete)")
	}
	switch args[0] {
	case "create", "update":
		if len(args) < 2 {
			die("zone %s: name required", args[0])
		}
		c.zoneWrite(ctx, args[0], args[1], args[2:])
	case "list":
		var zones []*sharddnsv1.Zone
		token := ""
		for {
			resp, err := c.client.ListZones(ctx, connect.NewRequest(&sharddnsv1.ListZonesRequest{PageSize: 500, PageToken: token}))
			if err != nil {
				die("list zones: %v", err)
			}
			zones = append(zones, resp.Msg.GetZones()...)
			if token = resp.Msg.GetNextPageToken(); token == "" {
				break
			}
		}
		printZones(zones)
	case "get":
		if len(args) < 2 {
			die("zone get: name required")
		}
		resp, err := c.client.GetZone(ctx, connect.NewRequest(&sharddnsv1.GetZoneRequest{Name: args[1]}))
		if err != nil {
			die("get zone: %v", err)
		}
		printZones([]*sharddnsv1.Zone{resp.Msg})
	case "delete", "del", "rm":
		if len(args) < 2 {
			die("zone delete: name required")
		}
		if _, err := c.client.DeleteZone(ctx, connect.NewRequest(&sharddnsv1.DeleteZoneRequest{Name: args[1]})); err != nil {
			die("delete zone: %v", err)
		}
		fmt.Printf("deleted zone %q\n", args[1])
	default:
		die("zone: unknown subcommand %q", args[0])
	}
}

func (c *cli) zoneWrite(ctx context.Context, op, name string, args []string) {
	fs := flag.NewFlagSet("zone "+op, flag.ExitOnError)
	ns := fs.String("ns", "", "primary name server (default ns1.<zone>.)")
	email := fs.String("email", "", "admin email (default hostmaster.<zone>.)")
	refresh := fs.Int("refresh", 0, "SOA refresh in seconds")
	retry := fs.Int("retry", 0, "SOA retry in seconds")
	expire := fs.Int("expire", 0, "SOA expire in seconds")
	ttl := fs.Int("ttl", 0, "SOA minimum TTL in seconds")
	_ = fs.Parse(args)

	var (
		zone *sharddnsv1.Zone
		err  error
	)
	if op == "create" {
		var resp *connect.Response[sharddnsv1.Zone]
		resp, err = c.client.CreateZone(ctx, connect.NewRequest(&sharddnsv1.CreateZoneRequest{
			Name: name, PrimaryNs: *ns, AdminEmail: *email,
			Refresh: int32(*refresh), Retry: int32(*retry), Expire: int32(*expire), MinimumTtl: int32(*ttl),
		}))
		if resp != nil {
			zone = resp.Msg
		}
	} else {
		var resp *connect.Response[sharddnsv1.Zone]
		resp, err = c.client.UpdateZone(ctx, connect.NewRequest(&sharddnsv1.UpdateZoneRequest{
			Name: name, PrimaryNs: *ns, AdminEmail: *email,
			Refresh: int32(*refresh), Retry: int32(*retry), Expire: int32(*expire), MinimumTtl: int32(*ttl),
		}))
		if resp != nil {
			zone = resp.Msg
		}
	}
	if err != nil {
		die("%s zone: %v", op, err)
	}
	printZones([]*sharddnsv1.Zone{zone})
}

func (c *cli) record(ctx context.Context, args []string) {
	if len(args) == 0 {
		die("record: subcommand required (add|list|get|delete)")
	}
	switch args[0] {
	case "add", "create":
		if len(args) < 6 {
			die("record add: usage <zone> <name> <type> <ttl> <rdata...>")
		}
		ttl, err := strconv.Atoi(args[4])
		if err != nil {
			die("ttl: %v", err)
		}
		resp, err := c.client.CreateRecord(ctx, connect.NewRequest(&sharddnsv1.CreateRecordRequest{
			Zone: args[1], Name: args[2], Type: args[3], Ttl: int32(ttl), Rdata: strings.Join(args[5:], " "),
		}))
		if err != nil {
			die("create record: %v", err)
		}
		printRecords([]*sharddnsv1.Record{resp.Msg})
	case "list":
		if len(args) < 2 {
			die("record list: zone required")
		}
		var records []*sharddnsv1.Record
		token := ""
		for {
			resp, err := c.client.ListRecords(ctx, connect.NewRequest(&sharddnsv1.ListRecordsRequest{Zone: args[1], PageSize: 1000, PageToken: token}))
			if err != nil {
				die("list records: %v", err)
			}
			records = append(records, resp.Msg.GetRecords()...)
			if token = resp.Msg.GetNextPageToken(); token == "" {
				break
			}
		}
		printRecords(records)
	case "get":
		if len(args) < 4 {
			die("record get: usage <zone> <name> <type>")
		}
		resp, err := c.client.GetRecords(ctx, connect.NewRequest(&sharddnsv1.GetRecordsRequest{Zone: args[1], Name: args[2], Type: args[3]}))
		if err != nil {
			die("get records: %v", err)
		}
		printRecords(resp.Msg.GetRecords())
	case "delete", "del", "rm":
		if len(args) < 5 {
			die("record delete: usage <zone> <name> <type> <rdata...>")
		}
		rdata := strings.Join(args[4:], " ")
		if _, err := c.client.DeleteRecord(ctx, connect.NewRequest(&sharddnsv1.DeleteRecordRequest{
			Zone: args[1], Name: args[2], Type: args[3], Rdata: rdata,
		})); err != nil {
			die("delete record: %v", err)
		}
		fmt.Printf("deleted record %s/%s %s %q\n", args[1], args[2], args[3], rdata)
	default:
		die("record: unknown subcommand %q", args[0])
	}
}

func printZones(zones []*sharddnsv1.Zone) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPRIMARY_NS\tADMIN_EMAIL\tSERIAL\tREFRESH\tRETRY\tEXPIRE\tMIN_TTL\tUPDATED_AT")
	for _, z := range zones {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n",
			z.GetName(), z.GetPrimaryNs(), z.GetAdminEmail(), z.GetSerial(),
			z.GetRefresh(), z.GetRetry(), z.GetExpire(), z.GetMinimumTtl(),
			z.GetUpdatedAt().AsTime().Format(time.RFC3339))
	}
	_ = w.Flush()
}

func printRecords(records []*sharddnsv1.Record) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ZONE\tNAME\tTYPE\tTTL\tRDATA")
	for _, r := range records {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", r.GetZone(), r.GetName(), r.GetType(), r.GetTtl(), r.GetRdata())
	}
	_ = w.Flush()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
