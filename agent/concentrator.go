// Concentrator
// https://en.wikipedia.org/wiki/Knelson_concentrator
// Gets an imperial shitton of traces, and outputs pre-computed data structures
// allowing to find the gold (stats) amongst the traces.
package main

import (
	"sync"
	"time"

	log "github.com/cihub/seelog"

	"github.com/DataDog/raclette/model"
)

// Concentrator is getting a stream of raw traces and producing some time-bucketed normalized statistics from them.
//  * inSpans, channel from which we consume spans and create stats
//  * outStats, channel where we return our computed stats
//	* bucketDuration, designates the length of a time bucket
//	* openBucket, array of stats buckets we keep in memory (fixed size and iterating over)
//  * currentBucket, the index of openBucket we're currently writing to
type Concentrator struct {
	// work channels
	inSpans  chan model.Span        // incoming spans to add to stats
	outStats chan model.StatsBucket // outgoing stats buckets
	outSpans chan model.Span        // spans that potentially need to be written with that time bucket

	// exit channels
	exit      chan bool
	exitGroup *sync.WaitGroup

	// configuration
	bucketDuration int
	eps            float64

	// internal data structs
	openBucket    [2]*model.StatsBucket
	currentBucket int32
}

// NewConcentrator returns a new Concentrator flushing at a bucketDuration secondspace
func NewConcentrator(bucketDuration int, eps float64, exit chan bool, exitGroup *sync.WaitGroup) *Concentrator {
	return &Concentrator{
		bucketDuration: bucketDuration,
		eps:            eps,
		exit:           exit,
		exitGroup:      exitGroup,
	}
}

// Init sets the channels for incoming spans and outgoing stats before starting
func (c *Concentrator) Init(inSpans chan model.Span, outStats chan model.StatsBucket, outSpans chan model.Span) {
	c.inSpans = inSpans
	c.outStats = outStats
	c.outSpans = outSpans
}

// Start initializes the first structures and starts consuming stuff
func (c *Concentrator) Start() {
	// First bucket needs to be initialized manually now
	c.openBucket[0] = model.NewStatsBucket(c.eps)

	go func() {
		// should return when upstream span channel is closed
		for s := range c.inSpans {
			c.HandleNewSpan(&s)
			c.outSpans <- s
		}
	}()

	go c.bucketCloser()

	log.Info("Concentrator started")
}

// HandleNewSpan adds to the current bucket the pointed span
func (c *Concentrator) HandleNewSpan(s *model.Span) {
	c.openBucket[c.currentBucket].HandleSpan(s)
}

func (c *Concentrator) flush() {
	nextBucket := (c.currentBucket + 1) % 2
	c.openBucket[nextBucket] = model.NewStatsBucket(c.eps)

	//FIXME: use a mutex? too slow? don't care about potential traces written to previous bucket?
	// Use it and close the previous one
	c.openBucket[c.currentBucket].Duration = model.Now() - c.openBucket[c.currentBucket].Start
	c.currentBucket = nextBucket

	// flush the other bucket before
	bucketToSend := (c.currentBucket + 1) % 2
	if c.openBucket[bucketToSend] != nil {
		// prepare for serialization
		c.openBucket[bucketToSend].Encode()
		c.outStats <- *c.openBucket[bucketToSend]
	}
}

func (c *Concentrator) bucketCloser() {
	// block on the closer, to flush cleanly last bucket
	c.exitGroup.Add(1)
	ticker := time.Tick(time.Duration(c.bucketDuration) * time.Second)
	for {
		select {
		case <-c.exit:
			log.Info("Concentrator exiting")
			// FIXME: don't flush, because downstream the writer is already shutting down
			// c.flush()

			// return cleanly and close writer chans
			close(c.outSpans)
			c.exitGroup.Done()
			return
		case <-ticker:
			log.Info("Concentrator flushed a time bucket")
			c.flush()
		}
	}
}