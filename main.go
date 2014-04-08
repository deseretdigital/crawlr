package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type Config struct {
	StartUrl		string
	DropHttps		bool
	AllowedDomains		map[string]bool
	RewriteDomains		map[string]string
	FilteredUrls		[]string
	CFilteredUrls		[]*regexp.Regexp
	DroppedParameters	[]string
	RequiredPatterns	map[string]string
	CRequiredPatterns	map[string]*regexp.Regexp
}

type Link struct {
	url			*url.URL
	depth			int
	referrer		string
}

const STATS_FREQ = 1

var quit = make(chan bool)
var linkQueue = make(chan *Link, 100000)
var workQueue = make(chan *Link, 100000)
var workerDone = make(chan bool)
var clientList = make(chan *http.Client, 100)

var config Config
var cookieJar *cookiejar.Jar
var droppedDomains map[string]bool = make(map[string]bool)

// flags
var workerMax = flag.Int("n", 1, "maximum concurrent requests")
var maxDepth = flag.Int("d", 2, "maximum depth")
var verbose = flag.Bool("v", false, "verbose output")
var showStats = flag.Bool("s", false, "show live stats")


func main() {
	runtime.GOMAXPROCS(4)
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Printf("Usage: spider <config file>\n")
		return
	}

	err := loadConfig(flag.Arg(0))
	if err != nil {
		fmt.Printf("ERROR: couldn't load configuration file\n")
		return
	}

	cookieJar, _ = cookiejar.New(nil)

	fmt.Printf("Spidering %s\n", config.StartUrl);

	sUrl, err := url.Parse(config.StartUrl)
	if err != nil {
		fmt.Printf("ERROR: couldn't parse URL %s\n", config.StartUrl)
		return
	}

	workerCount := 1
	go worker()
	go linkProcessor()

	startLink := Link{ url: sUrl, depth: 0, referrer: "" }
	linkQueue <-&startLink

	statsTimer := time.NewTimer(STATS_FREQ * time.Second)
	workDone := 0
	for {
		select {
		case <-workerDone:
			workerCount--
			workDone++
			workLength := len(workQueue) + len(linkQueue)
			if workLength == 0 {
				fmt.Println("Done spidering")
				goto doneSpidering
			}
			for i := 0; i < workLength && workerCount < *workerMax; i++ {
				workerCount++
				go worker()
			}
		case <-statsTimer.C:
			statsTimer = time.NewTimer(STATS_FREQ * time.Second)
			if *showStats {
				fmt.Printf("WORK QUEUE: %d/%d [%d workers, %d done]\n", len(workQueue), len(linkQueue), workerCount, workDone)
			}
		case <-quit:
			goto doneSpidering
		}
	}

	doneSpidering:

	for domain, _ := range droppedDomains {
		fmt.Printf("IGNORED DOMAIN: %s\n", domain);
	}

	return
}

func loadConfig(filename string) (e error) {
	file, err := os.Open(filename)
	if err != nil {
		fmt.Printf("failed to open config file %s\n", filename)
		e = err
		return
	}

	dec := json.NewDecoder(file)
	err = dec.Decode(&config)
	if err != nil {
		fmt.Printf("failed to decode\n")
		e = err
		return
	}

	config.CFilteredUrls = make([]*regexp.Regexp, len(config.FilteredUrls))
	for i, filter := range config.FilteredUrls {
		config.CFilteredUrls[i] = regexp.MustCompile(filter)
	}

	config.CRequiredPatterns = make(map[string]*regexp.Regexp)
	for key, pattern := range config.RequiredPatterns {
		config.CRequiredPatterns[key] = regexp.MustCompile(pattern)
	}

	if *verbose {
		fmt.Printf("Loaded Configuration:\n")
		fmt.Printf("%+v\n", config)
	}

	e = nil
	return
}

func linkProcessor() {
	visitedLinks := make(map[string]bool, 10000)

	for {
		acceptNewLink: inLink := <-linkQueue
		link := inLink.url
		//fmt.Printf("IN LINK: %s\n", link.String())

		if config.DropHttps && link.Scheme == "https" {
			//fmt.Printf("DROPPING HTTPS: %s\n", link.String())
			goto acceptNewLink
		}

		if link.Scheme == "" {
			link.Scheme = "http"
		}

		if link.Scheme != "http" && link.Scheme != "https" {
			goto acceptNewLink
		}

		// rewrite the domain, if necessary
		if newDomain, ok := config.RewriteDomains[link.Host]; ok {
			//fmt.Printf("REWRITE: %s => %s\n", link.Host, newDomain)
			link.Host = newDomain
		}

		// is this domain allowed?
		allowedDomain, ok := config.AllowedDomains[link.Host]
		if !ok || allowedDomain == false {
			//fmt.Printf("DOMAIN NOT ALLOWED: %s\n", link.Host)
			droppedDomains[link.Host] = true
			goto acceptNewLink
		}

		// translate &amp;'s
		link.RawQuery = strings.Replace(link.RawQuery, "&amp;", "&", -1)

		// FIXME
		if link.Path == "/index.php" {
			link.Path = "/"
		}
		if link.Path == "index.php" {
			link.Path = "/"
		}

		// remove filtered parameters
		queryValues := link.Query()
		for _, droppedParam := range config.DroppedParameters {
			queryValues.Del(droppedParam)
		}

		// re-encode query string, which also conveniently
		// normalizes the parameter order
		link.RawQuery = queryValues.Encode()

		linkString := link.String()

		// should this URL be dropped?
		for _, filter := range config.CFilteredUrls {
			if filter.MatchString(linkString) {
				//fmt.Printf("URL NOT ALLOWED: %s\n", linkString)
				goto acceptNewLink
			}
		}

		// remember that we've seen this link
		visited, ok := visitedLinks[linkString]
		if ok && visited {
			//fmt.Printf("ALREADY SEEN: %s\n", linkString)
			goto acceptNewLink
		}
		visitedLinks[linkString] = true

		//fmt.Printf("NEW LINK: %s\n", linkString)
		workQueue <-inLink
	}
}

func worker() {
	task := <-workQueue
	//fmt.Printf("Got task %s\n", task.String())
	spiderPage(task)
	workerDone <-true
}

func grabClient() (client *http.Client) {
	select {
	case client = <-clientList:

	default:
		client = new(http.Client)
		client.Jar = cookieJar
	}
	return
}

func releaseClient(client *http.Client) {
	clientList <-client
}

func spiderPage(inLink *Link) {
	var buf []byte

	task := inLink.url

	client := grabClient()

	startTime := time.Now()
	//fmt.Println("Performing request")
	resp, err := client.Get(task.String())
	if err != nil {
		fmt.Printf("ERROR: %s\n", task.String())
		return
	}

	defer resp.Body.Close()
	defer releaseClient(client)

	if _, ok := resp.Header["Content-Type"]; ok {
		if strings.HasPrefix(resp.Header["Content-Type"][0], "text/html") != true {
			return
		}
	}

	buf, err = ioutil.ReadAll(resp.Body)
	if len(buf) == 0 {
		return
	}
	requestDuration := time.Since(startTime)

	if *verbose {
		fmt.Printf("[%d, %s, %f] %s\n", resp.StatusCode, resp.Header["Content-Type"][0], requestDuration.Seconds(), task.String())
	}

	if inLink.depth < *maxDepth {
		re := regexp.MustCompile(`<a [^>]*href="([^"]+)"[^>]*>`)
		results := re.FindAllSubmatch(buf[0:], 1000)
		for _, result := range results {
			//fmt.Printf("%s\n", result[1])
			hrefUrl, err := url.Parse(string(result[1]))
			if err != nil {
				fmt.Printf("PARSE ERROR: %s\n", result[1])
				continue
			}

			// drop fragment-only url's
			hrefUrl.Fragment = ""
			if hrefUrl.String() == "" {
				continue
			}

			if hrefUrl.Scheme == "" {
				hrefUrl.Scheme = task.Scheme
			}
			if hrefUrl.Host == "" {
				hrefUrl.Host = task.Host
			}
			if hrefUrl.Path == "" {
				hrefUrl.Path = task.Path
			}

			//fmt.Printf("Scheme:%s Host:%s Path:%s Query:%s => %s\n", hrefUrl.Scheme, hrefUrl.Host, hrefUrl.Path, hrefUrl.RawQuery, hrefUrl.String())
			newLink := Link{ url: hrefUrl, depth: inLink.depth+1, referrer: inLink.url.String() }
			linkQueue <-&newLink
		}
	}

	checkForPatterns(buf, inLink)
}

func checkForPatterns(body []byte, link *Link) {
	failed := false

	for label, pattern := range config.CRequiredPatterns {
		found := pattern.Match(body)
		if !found {
			if !failed {
				failed = true
				fmt.Printf("FAIL %s", link.url.String())
			}
			fmt.Printf(" %s", label)
		}
	}
	if failed {
		fmt.Printf("\n")
	}
}

