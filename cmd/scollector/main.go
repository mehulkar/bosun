package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"bosun.org/_third_party/github.com/BurntSushi/toml"
	"bosun.org/cmd/scollector/collectors"
	"bosun.org/collect"
	"bosun.org/metadata"
	"bosun.org/opentsdb"
	"bosun.org/slog"
	"bosun.org/util"
)

// These constants should remain in source control as their zero values.
const (
	// VersionDate should be set at build time as a date: 20140721184001.
	VersionDate uint64 = 0
	// VersionID should be set at build time as the most recent commit hash.
	VersionID string = ""
)

var (
	flagFilter          = flag.String("f", "", "Filters collectors matching this term, multiple terms separated by comma. Works with all other arguments.")
	flagList            = flag.Bool("l", false, "List available collectors.")
	flagPrint           = flag.Bool("p", false, "Print to screen instead of sending to a host")
	flagBatchSize       = flag.Int("b", 0, "OpenTSDB batch size. Used for debugging bad data.")
	flagFake            = flag.Int("fake", 0, "Generates X fake data points on the test.fake metric per second.")
	flagDebug           = flag.Bool("d", false, "Enables debug output.")
	flagDisableMetadata = flag.Bool("m", false, "Disable sending of metadata.")
	flagVersion         = flag.Bool("version", false, "Prints the version and exits.")
	flagConf            = flag.String("conf", "", "Location of configuration file. Defaults to scollector.conf in directory of the scollector executable.")
	flagToToml          = flag.String("totoml", "", "Location of destination toml file to convert. Reads from value of -conf.")

	mains []func()
)

type Conf struct {
	// Host is the OpenTSDB or Bosun host to send data.
	Host string
	// FullHost enables full hostnames: doesn't truncate to first ".".
	FullHost bool
	// ColDir is the external collectors directory.
	ColDir string
	// Tags are added to every datapoint. If a collector specifies the same tag
	// key, this one will be overwritten. The host tag is not supported.
	Tags opentsdb.TagSet
	// Hostname overrides the system hostname.
	Hostname string
	// DisableSelf disables sending of scollector self metrics.
	DisableSelf bool
	// Freq is the default frequency in seconds for most collectors.
	Freq int
	// Filter filters collectors matching this term, multiple terms separated
	// by comma.
	Filter string

	// KeepalivedCommunity, if not empty, enables the Keepalived collector with
	// the specified community.
	KeepalivedCommunity string
	HAProxy             []HAProxy
	SNMP                []SNMP
	ICMP                []ICMP
	Vsphere             []Vsphere
	AWS                 []AWS
	Process             []collectors.ProcessParams
	ProcessDotNet       []ProcessDotNet
}

type HAProxy struct {
	User      string
	Password  string
	Instances []HAProxyInstance
}

type HAProxyInstance struct {
	Tier string
	URL  string
}

type ICMP struct {
	Host string
}

type Vsphere struct {
	Host     string
	User     string
	Password string
}

type AWS struct {
	AccessKey string
	SecretKey string
	Region    string
}

type SNMP struct {
	Community string
	Host      string
}

type ProcessDotNet struct {
	Name string
}

func readConf() *Conf {
	conf := &Conf{
		Host: "bosun",
	}
	loc := *flagConf
	if *flagConf == "" {
		p, err := exePath()
		if err != nil {
			slog.Error(err)
			return conf
		}
		dir := filepath.Dir(p)
		loc = filepath.Join(dir, "scollector.conf")
	}
	_, err := toml.DecodeFile(loc, &conf)
	if err != nil {
		if *flagConf != "" {
			slog.Fatal(err)
		}
		if *flagDebug {
			slog.Error(err)
		}
	}
	return conf
}

func main() {
	flag.Parse()
	if *flagToToml != "" {
		toToml(*flagToToml)
		fmt.Println("toml conversion complete; remove all empty values by hand (empty strings, 0)")
		return
	}
	if *flagPrint || *flagDebug {
		slog.Set(&slog.StdLog{Log: log.New(os.Stdout, "", log.LstdFlags)})
	}
	if *flagVersion {
		fmt.Printf("scollector version %v (%v)\n", VersionDate, VersionID)
		os.Exit(0)
	}
	for _, m := range mains {
		m()
	}
	conf := readConf()
	if !conf.Tags.Valid() {
		slog.Fatalf("invalid tags: %v", conf.Tags)
	} else if conf.Tags["host"] != "" {
		slog.Fatalf("host not supported in custom tags, use Hostname instead")
	}
	collectors.AddTags = conf.Tags
	util.FullHostname = conf.FullHost
	util.Set()
	if conf.Hostname != "" {
		util.Hostname = conf.Hostname
		if err := collect.SetHostname(conf.Hostname); err != nil {
			slog.Fatal(err)
		}
	}
	if conf.ColDir != "" {
		collectors.InitPrograms(conf.ColDir)
	}
	var err error
	check := func(e error) {
		if e != nil {
			err = e
		}
	}
	for _, h := range conf.HAProxy {
		for _, i := range h.Instances {
			collectors.HAProxy(h.User, h.Password, i.Tier, i.URL)
		}
	}
	for _, s := range conf.SNMP {
		check(collectors.SNMP(s.Community, s.Host))
	}
	for _, i := range conf.ICMP {
		check(collectors.ICMP(i.Host))
	}
	for _, a := range conf.AWS {
		check(collectors.AWS(a.AccessKey, a.SecretKey, a.Region))
	}
	for _, v := range conf.Vsphere {
		check(collectors.Vsphere(v.User, v.Password, v.Host))
	}
	for _, p := range conf.Process {
		check(collectors.AddProcessConfig(p))
	}
	if err != nil {
		slog.Fatal(err)
	}
	collectors.KeepalivedCommunity = conf.KeepalivedCommunity
	// Add all process collectors. This is platform specific.
	collectors.WatchProcesses()
	collectors.WatchProcessesDotNet()

	if *flagFake > 0 {
		collectors.InitFake(*flagFake)
	}
	collect.Debug = *flagDebug
	util.Debug = *flagDebug
	collect.DisableDefaultCollectors = conf.DisableSelf
	c := collectors.Search(*flagFilter)
	if len(c) == 0 {
		slog.Fatalf("Filter %s matches no collectors.", *flagFilter)
	}
	for _, col := range c {
		col.Init()
	}
	u, err := parseHost(conf.Host)
	if *flagList {
		list(c)
		return
	} else if err != nil {
		slog.Fatal("invalid host:", conf.Host)
	}
	if conf.Freq <= 0 {
		slog.Fatal("freq must be > 0")
	}
	collectors.DefaultFreq = time.Second * time.Duration(conf.Freq)
	collect.Freq = time.Second * time.Duration(conf.Freq)
	collect.Tags = opentsdb.TagSet{"os": runtime.GOOS}
	if *flagPrint {
		collect.Print = true
	}
	if !*flagDisableMetadata {
		if err := metadata.Init(u, *flagDebug); err != nil {
			slog.Fatal(err)
		}
	}
	cdp := collectors.Run(c)
	if u != nil {
		slog.Infoln("OpenTSDB host:", u)
	}
	if err := collect.InitChan(u, "scollector", cdp); err != nil {
		slog.Fatal(err)
	}
	if VersionDate > 0 {
		go func() {
			metadata.AddMetricMeta("scollector.version", metadata.Gauge, metadata.None,
				"Scollector version number, which indicates when scollector was built.")
			for {
				if err := collect.Put("version", collect.Tags, VersionDate); err != nil {
					slog.Error(err)
				}
				time.Sleep(time.Hour)
			}
		}()
	}
	if *flagBatchSize > 0 {
		collect.BatchSize = *flagBatchSize
	}
	go func() {
		const maxMem = 500 * 1024 * 1024 // 500MB
		var m runtime.MemStats
		for range time.Tick(time.Minute) {
			runtime.ReadMemStats(&m)
			if m.Alloc > maxMem {
				panic("memory max reached")
			}
		}
	}()
	select {}
}

func exePath() (string, error) {
	prog := os.Args[0]
	p, err := filepath.Abs(prog)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(p)
	if err == nil {
		if !fi.Mode().IsDir() {
			return p, nil
		}
		err = fmt.Errorf("%s is directory", p)
	}
	if filepath.Ext(p) == "" {
		p += ".exe"
		fi, err := os.Stat(p)
		if err == nil {
			if !fi.Mode().IsDir() {
				return p, nil
			}
			err = fmt.Errorf("%s is directory", p)
		}
	}
	return "", err
}

func list(cs []collectors.Collector) {
	for _, c := range cs {
		fmt.Println(c.Name())
	}
}

func parseHost(host string) (*url.URL, error) {
	if host == "" {
		host = "bosun"
	}
	if !strings.Contains(host, "//") {
		host = "http://" + host
	}
	return url.Parse(host)
}

func printPut(c chan *opentsdb.DataPoint) {
	for dp := range c {
		b, _ := json.Marshal(dp)
		slog.Info(string(b))
	}
}

func toToml(fname string) {
	var c Conf
	b, err := ioutil.ReadFile(*flagConf)
	if err != nil {
		slog.Fatal(err)
	}
	extra := new(bytes.Buffer)
	var hap HAProxy
	for i, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		sp := strings.SplitN(line, "=", 2)
		if len(sp) != 2 {
			slog.Fatalf("expected = in %v:%v", *flagConf, i+1)
		}
		k := strings.TrimSpace(sp[0])
		v := strings.TrimSpace(sp[1])
		switch k {
		case "host":
			c.Host = v
		case "hostname":
			c.Hostname = v
		case "filter":
			c.Filter = v
		case "coldir":
			c.ColDir = v
		case "snmp":
			for _, s := range strings.Split(v, ",") {
				sp := strings.Split(s, "@")
				if len(sp) != 2 {
					slog.Fatal("invalid snmp string:", v)
				}
				c.SNMP = append(c.SNMP, SNMP{
					Community: sp[0],
					Host:      sp[1],
				})
			}
		case "icmp":
			c.ICMP = append(c.ICMP, ICMP{v})

		case "haproxy":
			if v != "" {
				for _, s := range strings.Split(v, ",") {
					sp := strings.SplitN(s, ":", 2)
					if len(sp) != 2 {
						slog.Fatal("invalid haproxy string:", v)
					}
					if hap.User != "" || hap.Password != "" {
						slog.Fatal("only one haproxy line allowed")
					}
					hap.User = sp[0]
					hap.Password = sp[1]
				}
			}
		case "haproxy_instance":
			sp := strings.SplitN(v, ":", 2)
			if len(sp) != 2 {
				slog.Fatal("invalid haproxy_instance string:", v)
			}
			hap.Instances = append(hap.Instances, HAProxyInstance{
				Tier: sp[0],
				URL:  sp[1],
			})
		case "tags":
			tags, err := opentsdb.ParseTags(v)
			if err != nil {
				slog.Fatal(err)
			}
			c.Tags = tags
		case "aws":
			for _, s := range strings.Split(v, ",") {
				sp := strings.SplitN(s, ":", 2)
				if len(sp) != 2 {
					slog.Fatal("invalid AWS string:", v)
				}
				accessKey := sp[0]
				idx := strings.LastIndex(sp[1], "@")
				if idx == -1 {
					slog.Fatal("invalid AWS string:", v)
				}
				secretKey := sp[1][:idx]
				region := sp[1][idx+1:]
				if len(accessKey) == 0 || len(secretKey) == 0 || len(region) == 0 {
					slog.Fatal("invalid AWS string:", v)
				}
				c.AWS = append(c.AWS, AWS{
					AccessKey: accessKey,
					SecretKey: secretKey,
					Region:    region,
				})
			}
		case "vsphere":
			for _, s := range strings.Split(v, ",") {
				sp := strings.SplitN(s, ":", 2)
				if len(sp) != 2 {
					slog.Fatal("invalid vsphere string:", v)
				}
				user := sp[0]
				idx := strings.LastIndex(sp[1], "@")
				if idx == -1 {
					slog.Fatal("invalid vsphere string:", v)
				}
				pwd := sp[1][:idx]
				host := sp[1][idx+1:]
				if len(user) == 0 || len(pwd) == 0 || len(host) == 0 {
					slog.Fatal("invalid vsphere string:", v)
				}
				c.Vsphere = append(c.Vsphere, Vsphere{
					User:     user,
					Password: pwd,
					Host:     host,
				})
			}
		case "freq":
			freq, err := strconv.Atoi(v)
			if err != nil {
				slog.Fatal(err)
			}
			c.Freq = freq
		case "process":
			if runtime.GOOS == "linux" {
				var p struct {
					Command string
					Name    string
					Args    string
				}
				sp := strings.Split(v, ",")
				if len(sp) > 1 {
					p.Name = sp[1]
				}
				if len(sp) > 2 {
					p.Args = sp[2]
				}
				p.Command = sp[0]
				extra.WriteString(fmt.Sprintf(`
[[Process]]
  Command = %q
  Name = %q
  Args = %q
`, p.Command, p.Name, p.Args))
			} else if runtime.GOOS == "windows" {

				extra.WriteString(fmt.Sprintf(`
[[Process]]
  Name = %q
`, v))
			}
		case "process_dotnet":
			c.ProcessDotNet = append(c.ProcessDotNet, ProcessDotNet{v})
		case "keepalived_community":
			c.KeepalivedCommunity = v
		default:
			slog.Fatalf("unknown key in %v:%v", *flagConf, i+1)
		}
	}
	if len(hap.Instances) > 0 {
		c.HAProxy = append(c.HAProxy, hap)
	}

	f, err := os.Create(fname)
	if err != nil {
		log.Fatal(err)
	}
	if err := toml.NewEncoder(f).Encode(&c); err != nil {
		log.Fatal(err)
	}
	if _, err := extra.WriteTo(f); err != nil {
		log.Fatal(err)
	}
	f.Close()
}
