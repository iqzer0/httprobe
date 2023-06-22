package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type probeArgs []string

func (p *probeArgs) Set(val string) error {
	*p = append(*p, val)
	return nil
}

func (p probeArgs) String() string {
	return strings.Join(p, ",")
}

func main() {

	var concurrency int
	flag.IntVar(&concurrency, "c", 20, "set the concurrency level (split equally between HTTPS and HTTP requests)")

	var probes probeArgs
	flag.Var(&probes, "p", "add additional probe (proto:port)")

	var skipDefault bool
	flag.BoolVar(&skipDefault, "s", false, "skip the default probes (http:80 and https:443)")

	var to int
	flag.IntVar(&to, "t", 10000, "timeout (milliseconds)")

	var preferHTTPS bool
	flag.BoolVar(&preferHTTPS, "prefer-https", false, "only try plain HTTP if HTTPS fails")

	var method string
	flag.StringVar(&method, "method", "GET", "HTTP method to use")

	var concurrencyHigh int
	flag.IntVar(&concurrencyHigh, "ch", 1000, "set the high concurrency level for long timeout tasks")

	var toHigh int
	flag.IntVar(&toHigh, "th", 120000, "long timeout (milliseconds)")

	flag.Parse()

	timeout := time.Duration(to * 1000000)
	timeoutHigh := time.Duration(toHigh * 1000000)

	var tr = &http.Transport{
		MaxIdleConns:      30,
		IdleConnTimeout:   time.Second,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: time.Second,
		}).DialContext,
	}

	var trHigh = &http.Transport{
		MaxIdleConns:      30,
		IdleConnTimeout:   time.Second,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DialContext: (&net.Dialer{
			Timeout:   timeoutHigh,
			KeepAlive: time.Second,
		}).DialContext,
	}

	re := func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	client := &http.Client{
		Transport:     tr,
		CheckRedirect: re,
		Timeout:       timeout,
	}

	clientHigh := &http.Client{
		Transport:     trHigh,
		CheckRedirect: re,
		Timeout:       timeoutHigh,
	}

	httpsURLs := make(chan string)
	httpURLs := make(chan string)
	output := make(chan string)

	httpsURLsHigh := make(chan string)
	outputHigh := make(chan string)

	var httpsWG sync.WaitGroup
	for i := 0; i < concurrency/2; i++ {
		httpsWG.Add(1)

		go func() {
			for url := range httpsURLs {

				withProto := "https://" + url
				if isListening(client, withProto, method) {
					output <- withProto

					if preferHTTPS {
						continue
					}
				}

				httpURLs <- url
			}

			httpsWG.Done()
		}()
	}

	var httpsWGHigh sync.WaitGroup
	for i := 0; i < concurrencyHigh; i++ {
		httpsWGHigh.Add(1)

		go func() {
			for url := range httpsURLsHigh {
				withProto := "https://" + url
				if isListening(clientHigh, withProto, method) {
					outputHigh <- withProto
				}
			}

			httpsWGHigh.Done()
		}()
	}

	var httpWG sync.WaitGroup
	for i := 0; i < concurrency/2; i++ {
		httpWG.Add(1)

		go func() {
			for url := range httpURLs {
				withProto := "http://" + url
				if isListening(client, withProto, method) {
					output <- withProto
					continue
				}
			}

			httpWG.Done()
		}()
	}

	go func() {
		httpsWG.Wait()
		close(httpURLs)
	}()

	var outputWG sync.WaitGroup
	outputWG.Add(1)
	go func() {
		for o := range output {
			fmt.Println(o)
		}
		outputWG.Done()
	}()

	go func() {
		httpWG.Wait()
		close(output)
	}()

	go func() {
		httpsWGHigh.Wait()
		close(outputHigh)
	}()

	var outputWGHigh sync.WaitGroup
	outputWGHigh.Add(1)
	go func() {
		for o := range outputHigh {
			fmt.Println(o)
		}
		outputWGHigh.Done()
	}()

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		domain := strings.ToLower(sc.Text())

		if !skipDefault {
			httpsURLs <- domain
			httpsURLsHigh <- domain
		}

		xlarge := []string{"81", "300", "591", "593", "832", "981", "1010", "1311", "2082", "2087", "2095", "2096", "2480", "3000", "3128", "3333", "4243", "4567", "4711", "4712", "4993", "5000", "5104", "5108", "5800", "6543", "7000", "7396", "7474", "8000", "8001", "8008", "8014", "8042", "8069", "8080", "8081", "8088", "8090", "8091", "8118", "8123", "8172", "8222", "8243", "8280", "8281", "8333", "8443", "8500", "8834", "8880", "8888", "8983", "9000", "9043", "9060", "9080", "9090", "9091", "9200", "9443", "9800", "9981", "12443", "16080", "18091", "18092", "20720", "28017"}
		large := []string{"81", "591", "2082", "2087", "2095", "2096", "3000", "8000", "8001", "8008", "8080", "8083", "8443", "8834", "8888"}

		for _, p := range probes {
			switch p {
			case "xlarge":
				for _, port := range xlarge {
					httpsURLs <- fmt.Sprintf("%s:%s", domain, port)
					httpsURLsHigh <- fmt.Sprintf("%s:%s", domain, port)
				}
			case "large":
				for _, port := range large {
					httpsURLs <- fmt.Sprintf("%s:%s", domain, port)
					httpsURLsHigh <- fmt.Sprintf("%s:%s", domain, port)
				}
			default:
				pair := strings.SplitN(p, ":", 2)
				if len(pair) != 2 {
					continue
				}

				if strings.ToLower(pair[0]) == "https" {
					httpsURLs <- fmt.Sprintf("%s:%s", domain, pair[1])
					httpsURLsHigh <- fmt.Sprintf("%s:%s", domain, pair[1])
				} else {
					httpURLs <- fmt.Sprintf("%s:%s", domain, pair[1])
				}
			}
		}
	}

	close(httpsURLs)
	close(httpsURLsHigh)

	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to read input: %s\n", err)
	}

	outputWG.Wait()
	outputWGHigh.Wait()
}

func isListening(client *http.Client, url, method string) bool {

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return false
	}

	req.Header.Add("Connection", "close")
	req.Close = true

	resp, err := client.Do(req)
	if resp != nil {
		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()
	}

	if err != nil {
		return false
	}

	return true
}
