package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"reflect"
	"strconv"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/units"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
)

const (
	// name of the service
	name        = "host Stats"
	description = "collect host stats from multiple vcenter instances"
	debug       = false
)

type hostStat struct {
	Cluster            string
	Host               string
	Version            string
	Build              string
	Vendor             string
	Model              string
	NumCpuPkgs         int16
	NumCpuCores        int16
	NumCpuThreads      int16
	CpuModel           string
	TotalCPU           int64
	FreeCPU            int64
	OverallMemoryUsage int64
	MemorySize         int32
	FreeMemory         int64
}

func (r hostStat) Headers() []string {
	a := &hostStat{}
	var res []string
	val := reflect.ValueOf(a).Elem()
	for i := 0; i < val.NumField(); i++ {
		res = append(res, val.Type().Field(i).Name)
	}
	return res
}

func (r hostStat) Slice() []string {

	values := []string{
		r.Cluster,
		r.Host,
		r.Version,
		r.Build,
		r.Vendor,
		r.Model,
		strconv.FormatInt(int64(r.NumCpuPkgs), 10),
		strconv.FormatInt(int64(r.NumCpuCores), 10),
		strconv.FormatInt(int64(r.NumCpuThreads), 10),
		r.CpuModel,
		strconv.FormatInt(int64(r.TotalCPU), 10),
		strconv.FormatInt(int64(r.FreeCPU), 10),
		fmt.Sprintf("%s", (units.ByteSize(r.MemorySize))*1024*1024),
		fmt.Sprintf("%s", units.ByteSize(r.OverallMemoryUsage)),
		fmt.Sprintf("%s", units.ByteSize(r.FreeMemory)),
	}
	return values
}

// Configuration is used to store config data
type Configuration struct {
	Outpath  string
	VCenters []*VCenter
}

// VCenter for VMware vCenter connections
type VCenter struct {
	Hostname string
	Username string
	Password string
	client   *govmomi.Client
	Data     [][]string
	Worker   int
}

func main() {

	cfgFile := "config.json"

	// read the configuration
	file, err := os.Open(cfgFile)
	if err != nil {
		fmt.Println("Could not open configuration file", cfgFile)
	}

	jsondec := json.NewDecoder(file)
	config := Configuration{}
	err = jsondec.Decode(&config)
	if err != nil {
		fmt.Println("Could not decode configuration file", cfgFile)
	}

	//create csv with headers
	var Data hostStat
	headers := hostStat.Headers(Data)
	newCsv(headers, config.Outpath)
	//spew.Dump(config)

	// make the channels, get the time, launch the goroutines
	vcenterCount := len(config.VCenters)
	fmt.Println("Main :", vcenterCount, "vcenters to collect data from in config")
	vcenters := make(chan *VCenter, vcenterCount)
	done := make(chan bool, vcenterCount)

	fmt.Println("Main : Submitting job to workers")
	for i, vcenter := range config.VCenters {
		vcenter.Worker = i
		go worker(i, config, vcenters, done)
	}

	for _, vcenter := range config.VCenters {
		vcenters <- vcenter

	}
	close(vcenters)

	for i := 0; i < vcenterCount; i++ {
		<-done
	}
	//take the results and export them to csv file
	fmt.Println("Main : merging results...")
	for _, vcenter := range config.VCenters {
		fmt.Println("Main : worker", vcenter.Worker, "got", len(vcenter.Data), "results from", vcenter.Hostname)
		csvExport(vcenter.Data, config.Outpath)
	}
	fmt.Println("Main : Results saved to", config.Outpath)
}

func worker(id int, config Configuration, vcenters <-chan *VCenter, done chan<- bool) {
	for vcenter := range vcenters {

		fmt.Println("Worker", id, ": Received vcenter job", vcenter.Hostname)

		if err := vcenter.Connect(); err != nil {
			fmt.Println("Worker", id, ": Could not initialize connection to vcenter", vcenter.Hostname, err)
			done <- true
			continue
		}
		if err := vcenter.Init(config); err == nil {
			fmt.Println("Worker", id, ": Done", vcenter.Hostname)

		}

		vcenter.Disconnect()
		done <- true
	}

}

// Connect to the actual vCenter connection used to query data
func (vcenter *VCenter) Connect() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Println("Worker", vcenter.Worker, ": Connecting to vcenter:", vcenter.Hostname)
	u, err := url.Parse("https://" + vcenter.Username + ":" + vcenter.Password + "@" + vcenter.Hostname + "/sdk")
	if err != nil {
		fmt.Println("Worker", vcenter.Worker, ": Could not parse vcenter url:", vcenter.Hostname)
		fmt.Println("Error:", err)
		return err
	}

	client, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		fmt.Println("Worker", vcenter.Worker, ": Could not connect to vcenter:", vcenter.Hostname)
		fmt.Println("Error:", err)
		return err
	}

	vcenter.client = client

	return nil
}

// Disconnect from the vCenter
func (vcenter *VCenter) Disconnect() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if vcenter.client != nil {
		if err := vcenter.client.Logout(ctx); err != nil {
			fmt.Println("Worker", vcenter.Worker, ": Could not disconnect properly from vcenter:", vcenter.Hostname, err)
			return err
		}
	}

	return nil
}

// Init the VCenter connection
func (vcenter *VCenter) Init(config Configuration) error {
	fmt.Println("Worker", vcenter.Worker, ": Collecting data")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := vcenter.client

	// Create a view of HostSystem objects
	m := view.NewManager(client.Client)

	v, err := m.CreateContainerView(ctx, client.ServiceContent.RootFolder, []string{"HostSystem"}, true)

	if err != nil {
		log.Fatal(err)
	}

	defer v.Destroy(ctx)

	var hss []mo.HostSystem
	err = v.Retrieve(ctx, []string{"HostSystem"}, []string{"summary", "parent", "hardware", "config"}, &hss)
	if err != nil {
		log.Fatal(err)
	}

	pc := property.DefaultCollector(client.Client)

	for _, hs := range hss {
		var cluster mo.ManagedEntity
		err = pc.RetrieveOne(ctx, *hs.Parent, []string{"name"}, &cluster)
		if err != nil {
			log.Fatal(err)
		}
		totalCPU := int64(hs.Summary.Hardware.CpuMhz) * int64(hs.Summary.Hardware.NumCpuCores)
		freeCPU := int64(totalCPU) - int64(hs.Summary.QuickStats.OverallCpuUsage)
		freeMemory := int64(hs.Summary.Hardware.MemorySize) - (int64(hs.Summary.QuickStats.OverallMemoryUsage) * 1024 * 1024)
		stats := hostStat{
			Cluster:            cluster.Name,
			Host:               hs.Summary.Config.Name,
			Build:              hs.Config.Product.Build,
			Version:            hs.Config.Product.Version,
			Model:              hs.Hardware.SystemInfo.Model,
			Vendor:             hs.Hardware.SystemInfo.Vendor,
			TotalCPU:           totalCPU,
			FreeCPU:            freeCPU,
			NumCpuPkgs:         hs.Summary.Hardware.NumCpuPkgs,
			NumCpuCores:        hs.Summary.Hardware.NumCpuCores,
			NumCpuThreads:      hs.Summary.Hardware.NumCpuThreads,
			CpuModel:           hs.Summary.Hardware.CpuModel,
			MemorySize:         hs.Summary.QuickStats.OverallMemoryUsage,
			OverallMemoryUsage: hs.Summary.Hardware.MemorySize,
			FreeMemory:         freeMemory,
		}
		vcenter.Data = append(vcenter.Data, hostStat.Slice(stats))

	}

	return nil

}

func newCsv(headers []string, path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	if err := writer.Write(headers); err != nil {

	}
	return nil
}

func csvExport(data [][]string, path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	for _, value := range data {
		if err := writer.Write(value); err != nil {
			return err
		}
	}
	return nil
}
