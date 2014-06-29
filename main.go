// on osx: socat -d -d unix-l:/tmp/docker.sock,fork tcp:$(boot2docker ip 2>/dev/null):2375
package main

import(
  "flag"
  "fmt"
  "log"
  "strings"
  "errors"
  "time"
  "github.com/coreos/go-etcd/etcd"
  docker "github.com/fsouza/go-dockerclient"
)

var url string
var etcdUrl string
var publicIp string
var Env map[string]string
var services map[string]*Service
type Config struct {
  ttl time.Duration
}

type Service struct {
  ID       string
  path     string
  location string
  eclient  *etcd.Client
  stop     chan bool
  config   Config
}
func (s *Service) Close() {
  s.stop <- true
}
func (s *Service) Set() (err error) {
  fmt.Println("Set-ing")
  _, err = s.eclient.Set(s.path, s.location, uint64(s.config.ttl/(time.Second)))

  if err != nil {
    return err
  }
  return
}
func (s *Service) Announce() (err error) {
  err = s.Set()

  if err != nil {
    return err
  }
  tick := time.Tick(s.config.ttl/2)

  go s.heartbeat(tick)

  return
}
func (s *Service) heartbeat(tick <-chan time.Time) {
  for {
    select {
      case <- tick:
        s.Set()
      case <- s.stop:
        fmt.Printf("Shutting down heartbeat for %s\n", s.ID)
        return
      default:
        time.Sleep(50*time.Millisecond)
    }
  }
}
func initFlags() {
  flag.StringVar(&url, "url", "unix:///var/run/docker.sock", "docker sock path")
  flag.StringVar(&etcdUrl, "etcd-url", "http://127.0.0.1:4001", "etcd url")
  flag.StringVar(&publicIp, "public-ip", "localhost", "public ip")

  flag.Parse()
}
func itemFromArray(list []docker.APIContainers, ID string) (docker.APIContainers, error) {
  for _, c := range list {
    if c.ID == ID {
      return c, nil
    }
  }
  return docker.APIContainers{}, errors.New(ID + " cannot be found")
}

func addContainer(ID string, client *docker.Client, eclient *etcd.Client, config Config, services map[string]*Service) {
  containers, _ :=  client.ListContainers(docker.ListContainersOptions{
    All: false,
  })
  service, err := itemFromArray(containers, ID)

  if err != nil {
    fmt.Printf("Failed to get container: %s - %s", ID, err)
    return
  }
  image := service.Image
  parts := strings.Split(image, ":")
  name := parts[0]
  version := parts[1]
  //fmt.Printf("NAME: %s VERSION: %s - %s\n", name, version, c.ID)
  container, err := client.InspectContainer(service.ID)
  if err != nil {
    log.Fatal("Failed to inspect container", service.ID, err)
    return
  }
  fmt.Printf("Networking %d\n", len(container.NetworkSettings.Ports))
  if len(container.NetworkSettings.Ports) < 1 {
    return
  }

  mappedPorts, ok := container.NetworkSettings.Ports[docker.Port("8080/tcp")]
  if ok == false {
    fmt.Println("no port exposed for 8080")
  } else {
    mappedPort := mappedPorts[0]
    myPort := mappedPort.HostPort
    fmt.Printf("len: %s", mappedPort.HostPort)
    s := &Service{
      service.ID,
      fmt.Sprintf("%s/v%s", name, version),
      fmt.Sprintf("http://%s:%s", publicIp, myPort),
      eclient,
      make(chan bool),
      config}

    s.Announce()
    services[s.ID] = s
  }
}

func removeContainer(ID string, services map[string]*Service) {
  service, ok := services[ID]
  if ok {
    service.Close()
    delete(services, ID)
    fmt.Printf("Container(%s) removed\n", ID)
  } else {
    fmt.Printf("Cannot find: %s\n", ID)
  }
}

func addServices(client *docker.Client, eclient *etcd.Client, config Config, services map[string]*Service) {
  containers, _ := client.ListContainers(docker.ListContainersOptions{
    All: false,
  })

  for _, c := range containers {
    fmt.Printf("addContainer(%s)", c.ID)
    s := &Service{c.ID, "/foo/bar/v1.2.3", "http://suchland:1337", eclient, make(chan bool), config}
    s.Announce()
    services[s.ID] = s
  }

}

func main() {
  initFlags()
  
  fmt.Printf("Using socket: %s\n", url)
  config := Config{
    ttl: 3000 * time.Millisecond,
  }

  client, err := docker.NewClient(url)

  if err != nil {
    log.Fatalf("Unable to parse %s: %s", url, err)
  }

  etcdClient := etcd.NewClient([]string{etcdUrl})

  services = make(map[string]*Service)

  addServices(client, etcdClient, config, services)

  events := make(chan *docker.APIEvents)

  client.AddEventListener(events)

  for msg := range events {
    switch msg.Status {
      case "die":
        fmt.Printf("Should remove container: %s\n", msg.ID)
        removeContainer(msg.ID, services)
      case "start":
        fmt.Printf("Should add container: %s\n", msg.ID)
        addContainer(msg.ID, client, etcdClient, config, services)
    }
  }
}