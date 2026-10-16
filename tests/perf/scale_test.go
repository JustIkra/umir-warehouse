//go:build perf

package perf

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/JustIkra/umir-warehouse/contract"
	"github.com/JustIkra/umir-warehouse/internal/application"
)

const (
	products = 30
	dishes   = 40
	// commitSamples orders are timed in each of commitBlocks blocks that end at
	// every checkpoint.
	commitSamples = 20
	commitBlocks  = 3
	// deliveryEvery orders a delivery arrives.
	deliveryEvery = 150
	// movementCalls is how many Movements calls make up one timed run.
	movementCalls = 10
	// repeats takes the best of several runs of a read so scheduling noise does
	// not decide a ratio.
	repeats = 5
)

type row struct {
	orders, batches                                      int
	build, commitAvg, commitP50, menu, supply, recInputs time.Duration
	inventory, history, movements, recoverLive           time.Duration
	recoverJournal, recoverSnapshot                      time.Duration
	// movementsCPU and recoverJournalCPU are the process CPU time of the same
	// runs: other processes on a busy machine do not change it.
	movementsCPU, recoverJournalCPU time.Duration
	heapOwnerMiB, heapProcessMiB    float64
}

// ingredient is the product and quantity of one ingredient of a dish.
func ingredient(dish, k int) (int, contract.Quantity) {
	return (dish*7 + k*3) % products, contract.Quantity(1000 * (k + 1))
}

// demandPerCycle is what each product is consumed by the orders of one cycle of
// the corpus: commit orders every dish once with one portion and once with two.
func demandPerCycle() [products]contract.Quantity {
	var demand [products]contract.Quantity
	for dish := range dishes {
		for k := range 4 {
			product, quantity := ingredient(dish, k)
			demand[product] += 3 * quantity
		}
	}
	return demand
}

// deliver brings each product what the next deliveryEvery orders use plus a
// margin, so batches run out as they would in a kitchen and the number of
// batches an order can draw from does not grow with the number of orders.
func (s *stand) deliver() {
	s.t.Helper()
	now := s.clock.Now()
	lines := make([]contract.DeliveryLine, 0, products)
	for p, demand := range demandPerCycle() {
		if demand == 0 {
			continue
		}
		quantity := demand * deliveryEvery / dishes * 13 / 10
		lines = append(lines, contract.DeliveryLine{ProductID: "p" + strconv.Itoa(p), ManufacturedAt: now.Add(-time.Hour), ShelfLife: contract.ShelfLife{Value: 12, Unit: contract.ShelfLifeMonth}, ReceivedAt: now, Quantity: quantity})
	}
	if _, err := s.owner.ReceiveDelivery(s.ctx, contract.ReceiveDeliveryRequest{Scope: s.scope, CommandID: newUUID(), DeliveryID: newUUID(), Number: "N", CreatedAt: now, Lines: lines}); err != nil {
		s.t.Fatal(err)
	}
	s.batches += len(lines)
}

func (s *stand) seedCatalog() {
	s.t.Helper()
	for p := range products {
		if _, err := s.owner.RegisterProduct(s.ctx, contract.RegisterProductRequest{CommandID: newUUID(), ProductID: "p" + strconv.Itoa(p), Name: "Product " + strconv.Itoa(p), BaseUnit: contract.BaseUnitG}); err != nil {
			s.t.Fatal(err)
		}
	}
	for d := range dishes {
		ingredients := make([]contract.Ingredient, 0, 4)
		for k := range 4 {
			product, quantity := ingredient(d, k)
			ingredients = append(ingredients, contract.Ingredient{ProductID: "p" + strconv.Itoa(product), Quantity: quantity})
		}
		if _, err := s.owner.DefineDish(s.ctx, contract.DefineDishRequest{CommandID: newUUID(), DishID: "d" + strconv.Itoa(d), Name: "Dish " + strconv.Itoa(d), Ingredients: ingredients}); err != nil {
			s.t.Fatal(err)
		}
	}
	s.deliver()
}

func (s *stand) commit(i int) {
	s.t.Helper()
	positions := []contract.AcceptedPosition{{LineNo: 1, DishID: "d" + strconv.Itoa(i%dishes), Portions: 1}, {LineNo: 2, DishID: "d" + strconv.Itoa((i*3+1)%dishes), Portions: 2}}
	result, err := s.owner.CommitAcceptedOrder(s.ctx, contract.CommitOrderRequest{CommandID: newUUID(), ProposalID: newUUID(), AcceptedPositions: positions})
	if err != nil {
		s.t.Fatal(err)
	}
	if result.Kind != contract.CommitResultOrderCommitted {
		s.deliver()
		result, err = s.owner.CommitAcceptedOrder(s.ctx, contract.CommitOrderRequest{CommandID: newUUID(), ProposalID: newUUID(), AcceptedPositions: positions})
		if err != nil || result.Kind != contract.CommitResultOrderCommitted {
			s.t.Fatalf("commit %d: %v %v", i, result.Kind, err)
		}
	}
	s.clock.Advance(time.Minute)
}

func (s *stand) timed(name string, f func() error) time.Duration {
	s.t.Helper()
	start := time.Now()
	if err := f(); err != nil {
		s.t.Fatalf("%s: %v", name, err)
	}
	return time.Since(start)
}

// span is the wall-clock and the CPU time of one run.
type span struct{ wall, cpu time.Duration }

// cpuTime is the user and system CPU time the process has used.
func cpuTime() time.Duration {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

func (s *stand) timedSpan(name string, f func() error) span {
	s.t.Helper()
	cpu := cpuTime()
	wall := s.timed(name, f)
	return span{wall: wall, cpu: cpuTime() - cpu}
}

func (s *stand) best(name string, f func() error) span {
	s.t.Helper()
	shortest := s.timedSpan(name, f)
	for range repeats - 1 {
		run := s.timedSpan(name, f)
		shortest = span{min(shortest.wall, run.wall), min(shortest.cpu, run.cpu)}
	}
	return shortest
}

// recoverFresh times Recover of a new owner over the same journal; the owner
// is built outside the timer. A nil snapshot store replays the whole journal.
func (s *stand) recoverFresh(name string, withSnapshots bool) span {
	s.t.Helper()
	var shortest span
	for attempt := range repeats {
		var owner *application.Owner
		if withSnapshots {
			owner = s.newOwner(s.snapshots)
		} else {
			owner = s.newOwner(nil)
		}
		runtime.GC() // garbage of the previous run must not be collected inside the timer
		run := s.timedSpan(name, func() error { return owner.Recover(s.ctx) })
		if attempt == 0 {
			shortest = run
		}
		shortest = span{min(shortest.wall, run.wall), min(shortest.cpu, run.cpu)}
	}
	return shortest
}

// freshOwnerHeap is the live heap a restored owner adds over the shared journal.
func (s *stand) freshOwnerHeap() float64 {
	s.t.Helper()
	before := heapMiB()
	owner := s.newOwner(s.snapshots)
	if err := owner.Recover(s.ctx); err != nil {
		s.t.Fatal(err)
	}
	after := heapMiB()
	runtime.KeepAlive(owner)
	return after - before
}

func TestScale(t *testing.T) {
	s := newStand(t)
	baseHeap := heapMiB()
	actor := newUUID()
	s.seedCatalog()
	planning := contract.PlanningRequest{Scope: s.scope, Actor: actor}
	analytics := contract.AnalyticsRequest{Scope: s.scope, Actor: actor, ExpectedSettingsVersion: "1"}

	var rows []row
	done := 0
	start := time.Now()
	for _, checkpoint := range []int{1000, 4000, 8000} {
		for ; done < checkpoint-commitSamples*commitBlocks; done++ {
			if done%deliveryEvery == deliveryEvery-1 {
				s.deliver()
			}
			s.commit(done)
			s.flushSnapshots()
		}
		r := row{orders: checkpoint, batches: s.batches}
		// The median of a block is steady against a few preempted commits, and the
		// best block against a busy machine.
		for block := range commitBlocks {
			var blockTotal time.Duration
			samples := make([]time.Duration, 0, commitSamples)
			for range commitSamples {
				sample := s.timed("commit", func() error { s.commit(done); return nil })
				samples = append(samples, sample)
				blockTotal += sample
				s.flushSnapshots()
				done++
			}
			slices.Sort(samples)
			avg, p50 := blockTotal/commitSamples, samples[commitSamples/2]
			if block == 0 || avg < r.commitAvg {
				r.commitAvg = avg
			}
			if block == 0 || p50 < r.commitP50 {
				r.commitP50 = p50
			}
		}
		r.build = time.Since(start)
		r.menu = s.timed("menu", func() error { _, err := s.owner.EvaluateMenu(s.ctx); return err })
		r.supply = s.timed("supply", func() error { _, err := s.planning.Supply(s.ctx, planning); return err })
		r.recInputs = s.timed("recommendations", func() error { _, err := s.planning.Recommendations(s.ctx, planning); return err })
		r.inventory = s.timed("inventory", func() error { _, err := s.owner.GetInventory(s.ctx); return err })
		r.history = s.timed("history", func() error { _, err := s.owner.GetHistory(s.ctx); return err })
		// A single call is too short to time on a small corpus, so a run is a batch of calls.
		movements := s.best("movements", func() error {
			for range movementCalls {
				if _, err := s.analytics.Movements(s.ctx, analytics); err != nil {
					return err
				}
			}
			return nil
		})
		r.movements, r.movementsCPU = movements.wall/movementCalls, movements.cpu/movementCalls
		r.recoverLive = s.timed("recover", func() error { return s.owner.Recover(s.ctx) })
		journal := s.recoverFresh("recover_journal", false)
		r.recoverJournal, r.recoverJournalCPU = journal.wall, journal.cpu
		r.recoverSnapshot = s.recoverFresh("recover_snapshot", true).wall
		r.heapOwnerMiB = s.freshOwnerHeap()
		r.heapProcessMiB = heapMiB() - baseHeap
		rows = append(rows, r)
	}

	var table strings.Builder
	w := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	check := func(_ int, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(fmt.Fprintln(w, "orders\tbatches\tbuild\tcommit_avg\tcommit_p50\tmenu\tsupply\trecInputs\tinventory\thistory\tmovements\trecover\trecover_journal\trecover_snapshot\theap_owner_MiB\theap_process_MiB"))
	for _, r := range rows {
		check(fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%.1f\t%.1f\n", r.orders, r.batches,
			r.build.Round(time.Millisecond), r.commitAvg.Round(time.Microsecond), r.commitP50.Round(time.Microsecond), r.menu.Round(time.Microsecond), r.supply.Round(time.Microsecond), r.recInputs.Round(time.Microsecond),
			r.inventory.Round(time.Microsecond), r.history.Round(time.Microsecond), r.movements.Round(time.Millisecond), r.recoverLive.Round(time.Millisecond),
			r.recoverJournal.Round(time.Millisecond), r.recoverSnapshot.Round(time.Millisecond), r.heapOwnerMiB, r.heapProcessMiB))
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", table.String())

	first, last := rows[0], rows[len(rows)-1]
	ratio := func(name string, small, large time.Duration, limit float64) {
		t.Helper()
		got := float64(large) / float64(small)
		t.Logf("%s(%d)/%s(%d) = %.2f (limit %.0f)", name, last.orders, name, first.orders, got, limit)
		if got >= limit {
			t.Errorf("%s grew %.2f times from %d to %d orders; want under %.0f", name, got, first.orders, last.orders, limit)
		}
	}
	// The wall-clock ratios are printed; the limits apply to CPU time, which a
	// busy machine does not stretch.
	t.Logf("wall: recover_journal %.2f, movements %.2f", float64(last.recoverJournal)/float64(first.recoverJournal), float64(last.movements)/float64(first.movements))
	ratio("recover_journal_cpu", first.recoverJournalCPU, last.recoverJournalCPU, 12)
	// Movements reads and sums every event of the period, so it grows with the
	// corpus (8 times) and a little more as the events stop fitting in cache. It
	// was 53 times before the period was read directly.
	ratio("movements_cpu", first.movementsCPU, last.movementsCPU, 20)
	// The commit ratio is checked on the median of the timed commits: on a busy
	// machine the mean is decided by a few preempted samples. The best-block
	// mean ratio is printed too.
	t.Logf("commit_avg(%d)/commit_avg(%d) = %.2f", last.orders, first.orders, float64(last.commitAvg)/float64(first.commitAvg))
	ratio("commit_p50", first.commitP50, last.commitP50, 3)
}
