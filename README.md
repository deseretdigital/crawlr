crawlr
======

A web spider, written in Go, that scans your site looking for pages that are
missing particular string patterns.

## Configuration

The configuration file is passed to the spider as its first command-line
parameter.  The file should contain a JSON object with the following keys:

* startUrl
* dropHttps
* allowedDomains
* rewriteDomains
* filteredUrls
* droppedParameters
* requiredPatterns

An example JSON configuration file is included in the repo.

## Options

You can also change certain operational details of the spider through the
use of command-line parameters:

* -h : print list of accepted parameters
* -d : how deep to spider the site (default:2)
* -n : maximum concurrent requests (default:1)
* -s : show live stats (default:false)
* -v : verbose output (default:false)

## Output

As the spider progresses, it will print out the URL's of pages that didn't
contain the required patterns, as well as the list of patterns that weren't
found.

