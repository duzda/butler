package singlediff

import (
	"os"
	"path/filepath"
	"time"

	humanize "github.com/dustin/go-humanize"
	"github.com/itchio/wharf/bsdiff"
	"github.com/itchio/wharf/pwr"
	"github.com/itchio/wharf/wire"
	"github.com/pkg/errors"

	"github.com/itchio/butler/comm"
	"github.com/itchio/butler/mansion"
	"github.com/itchio/wharf/tlc"
)

var args = struct {
	old         string
	new         string
	output      string
	partitions  int
	concurrency int
}{}

func Register(ctx *mansion.Context) {
	cmd := ctx.App.Command("singlediff", "(Advanced) generate a wharf patch with bsdiff between two files")
	cmd.Arg("old", "Old file").Required().StringVar(&args.old)
	cmd.Arg("new", "New file").Required().StringVar(&args.new)
	cmd.Flag("output", "Patch file to write").Short('o').Required().StringVar(&args.output)
	cmd.Flag("partitions", "Number of partitions to use").Default("1").IntVar(&args.partitions)
	cmd.Flag("concurrency", "Suffix sort concurrency").Default("1").IntVar(&args.concurrency)
	ctx.Register(cmd, func(ctx *mansion.Context) {
		ctx.Must(do(ctx))
	})
}

func do(ctx *mansion.Context) error {
	consumer := comm.NewStateConsumer()

	if filepath.IsAbs(args.old) {
		return errors.Errorf("%s: singlediff only works with relative paths", args.old)
	}
	if filepath.IsAbs(args.new) {
		return errors.Errorf("%s: singlediff only works with relative paths", args.new)
	}

	oldfile, err := os.Open(args.old)
	if err != nil {
		return err
	}

	oldstats, err := oldfile.Stat()
	if err != nil {
		return err
	}

	newfile, err := os.Open(args.new)
	if err != nil {
		return err
	}

	newstats, err := newfile.Stat()
	if err != nil {
		return err
	}

	targetContainer := &tlc.Container{}
	targetContainer.Size = oldstats.Size()
	targetContainer.Files = []*tlc.File{
		&tlc.File{
			Mode:   0644,
			Offset: 0,
			Size:   oldstats.Size(),
			Path:   args.old,
		},
	}

	sourceContainer := &tlc.Container{}
	sourceContainer.Size = newstats.Size()
	sourceContainer.Files = []*tlc.File{
		&tlc.File{
			Mode:   0644,
			Offset: 0,
			Size:   newstats.Size(),
			Path:   args.new,
		},
	}

	consumer.Infof("Before: %s (%s)", humanize.IBytes(uint64(targetContainer.Size)), targetContainer.Stats())
	consumer.Infof(" After: %s (%s)", humanize.IBytes(uint64(sourceContainer.Size)), sourceContainer.Stats())

	writer, err := os.Create(args.output)
	if err != nil {
		return err
	}

	rawPatchWire := wire.NewWriteContext(writer)
	err = rawPatchWire.WriteMagic(pwr.PatchMagic)
	if err != nil {
		return nil
	}

	compression := &pwr.CompressionSettings{
		Algorithm: pwr.CompressionAlgorithm_BROTLI,
		Quality:   1,
	}

	header := &pwr.PatchHeader{
		Compression: compression,
	}

	err = rawPatchWire.WriteMessage(header)
	if err != nil {
		return nil
	}

	patchWire, err := pwr.CompressWire(rawPatchWire, compression)
	if err != nil {
		return nil
	}

	err = patchWire.WriteMessage(targetContainer)
	if err != nil {
		return err
	}

	err = patchWire.WriteMessage(sourceContainer)
	if err != nil {
		return err
	}

	syncHeader := &pwr.SyncHeader{
		FileIndex: 0,
		Type:      pwr.SyncHeader_BSDIFF,
	}
	err = patchWire.WriteMessage(syncHeader)
	if err != nil {
		return err
	}

	bsdiffHeader := &pwr.BsdiffHeader{
		TargetIndex: 0,
	}
	err = patchWire.WriteMessage(bsdiffHeader)
	if err != nil {
		return err
	}

	consumer.Opf("Suffix sort concurrency: %d, partitions: %d", args.concurrency, args.partitions)
	bdc := &bsdiff.DiffContext{
		SuffixSortConcurrency: args.concurrency,
		Partitions:            args.partitions,
		MeasureMem:            true,

		Stats: &bsdiff.DiffStats{},
	}

	startTime := time.Now()

	comm.StartProgress()
	err = bdc.Do(oldfile, newfile, patchWire.WriteMessage, consumer)
	comm.EndProgress()
	if err != nil {
		return err
	}

	err = patchWire.WriteMessage(&pwr.SyncOp{
		Type: pwr.SyncOp_HEY_YOU_DID_IT,
	})
	if err != nil {
		return err
	}

	err = patchWire.Close()
	if err != nil {
		return err
	}

	outStats, err := os.Stat(args.output)
	if err != nil {
		return err
	}

	duration := time.Since(startTime)
	perSec := float64(outStats.Size()) / duration.Seconds()

	consumer.Statf("Wrote %s patch to %s @ %s / s (%s total)",
		humanize.IBytes(uint64(outStats.Size())),
		args.output,
		humanize.IBytes(uint64(perSec)),
		duration,
	)

	consumer.Statf("Spent %s scanning", bdc.Stats.TimeSpentScanning)
	consumer.Statf("Spent %s sorting", bdc.Stats.TimeSpentSorting)
	return nil
}
