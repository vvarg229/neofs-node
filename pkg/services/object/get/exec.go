package getsvc

import (
	"context"
	"crypto/ecdsa"
	"errors"

	clientcore "github.com/nspcc-dev/neofs-node/pkg/core/client"
	"github.com/nspcc-dev/neofs-node/pkg/core/object"
	"github.com/nspcc-dev/neofs-node/pkg/services/object/util"
	"github.com/nspcc-dev/neofs-node/pkg/services/object_manager/placement"
	cid "github.com/nspcc-dev/neofs-sdk-go/container/id"
	objectSDK "github.com/nspcc-dev/neofs-sdk-go/object"
	oid "github.com/nspcc-dev/neofs-sdk-go/object/id"
	"go.uber.org/zap"
)

type statusError struct {
	status int
	err    error
}

type execCtx struct {
	svc *Service

	ctx context.Context

	prm          RangePrm
	prmRangeHash *RangeHashPrm

	statusError

	infoSplit *objectSDK.SplitInfo

	log *zap.Logger

	collectedObject *objectSDK.Object

	curOff uint64

	head bool

	curProcEpoch uint64
}

type execOption func(*execCtx)

const (
	statusUndefined int = iota
	statusOK
	statusINHUMED
	statusVIRTUAL
	statusOutOfRange
)

func headOnly() execOption {
	return func(c *execCtx) {
		c.head = true
	}
}

func withPayloadRange(r *objectSDK.Range) execOption {
	return func(c *execCtx) {
		c.prm.rng = r
	}
}

func withHash(p *RangeHashPrm) execOption {
	return func(ctx *execCtx) {
		ctx.prmRangeHash = p
	}
}

func (exec *execCtx) setLogger(l *zap.Logger) {
	req := "GET"
	if exec.headOnly() {
		req = "HEAD"
	} else if exec.ctxRange() != nil {
		req = "GET_RANGE"
	}

	exec.log = l.With(
		zap.String("request", req),
		zap.Stringer("address", exec.address()),
		zap.Bool("raw", exec.isRaw()),
		zap.Bool("local", exec.isLocal()),
		zap.Bool("with session", exec.prm.common.SessionToken() != nil),
		zap.Bool("with bearer", exec.prm.common.BearerToken() != nil),
	)
}

func (exec execCtx) context() context.Context {
	return exec.ctx
}

func (exec execCtx) isLocal() bool {
	return exec.prm.common.LocalOnly()
}

func (exec execCtx) isRaw() bool {
	return exec.prm.raw
}

func (exec execCtx) address() oid.Address {
	return exec.prm.addr
}

// isChild checks if reading object is a parent of the given object.
// Object without reference to the parent (only children with the parent header
// have it) is automatically considered as child: this should be guaranteed by
// upper level logic.
func (exec execCtx) isChild(obj *objectSDK.Object) bool {
	par := obj.Parent()
	return par == nil || equalAddresses(exec.address(), object.AddressOf(par))
}

func (exec execCtx) key() (*ecdsa.PrivateKey, error) {
	if exec.prm.signerKey != nil {
		// the key has already been requested and
		// cached in the previous operations
		return exec.prm.signerKey, nil
	}

	var sessionInfo *util.SessionInfo

	if tok := exec.prm.common.SessionToken(); tok != nil {
		sessionInfo = &util.SessionInfo{
			ID:    tok.ID(),
			Owner: tok.Issuer(),
		}
	}

	return exec.svc.keyStore.GetKey(sessionInfo)
}

func (exec *execCtx) canAssemble() bool {
	return exec.svc.assembly && !exec.isRaw() && !exec.headOnly() && !exec.isLocal()
}

func (exec *execCtx) splitInfo() *objectSDK.SplitInfo {
	return exec.infoSplit
}

func (exec *execCtx) containerID() cid.ID {
	return exec.address().Container()
}

func (exec *execCtx) ctxRange() *objectSDK.Range {
	return exec.prm.rng
}

func (exec *execCtx) headOnly() bool {
	return exec.head
}

func (exec *execCtx) netmapEpoch() uint64 {
	return exec.prm.common.NetmapEpoch()
}

func (exec *execCtx) netmapLookupDepth() uint64 {
	return exec.prm.common.NetmapLookupDepth()
}

func (exec *execCtx) initEpoch() bool {
	exec.curProcEpoch = exec.netmapEpoch()
	if exec.curProcEpoch > 0 {
		return true
	}

	e, err := exec.svc.currentEpochReceiver.currentEpoch()

	switch {
	default:
		exec.status = statusUndefined
		exec.err = err

		exec.log.Debug("could not get current epoch number",
			zap.String("error", err.Error()),
		)

		return false
	case err == nil:
		exec.curProcEpoch = e
		return true
	}
}

func (exec *execCtx) generateTraverser(addr oid.Address) (*placement.Traverser, bool) {
	obj := addr.Object()

	t, err := exec.svc.traverserGenerator.GenerateTraverser(addr.Container(), &obj, exec.curProcEpoch)

	switch {
	default:
		exec.status = statusUndefined
		exec.err = err

		exec.log.Debug("could not generate container traverser",
			zap.String("error", err.Error()),
		)

		return nil, false
	case err == nil:
		return t, true
	}
}

func (exec *execCtx) getChild(id oid.ID, rng *objectSDK.Range, withHdr bool) (*objectSDK.Object, bool) {
	w := NewSimpleObjectWriter()

	p := exec.prm
	p.common = p.common.WithLocalOnly(false)
	p.objWriter = w
	p.SetRange(rng)

	p.addr.SetContainer(exec.containerID())
	p.addr.SetObject(id)

	exec.statusError = exec.svc.get(exec.context(), p.commonPrm, withPayloadRange(rng))

	child := w.Object()
	ok := exec.status == statusOK

	if ok && withHdr && !exec.isChild(child) {
		exec.status = statusUndefined
		exec.err = errors.New("wrong child header")

		exec.log.Debug("parent address in child object differs")
	}

	return child, ok
}

func (exec *execCtx) headChild(id oid.ID) (*objectSDK.Object, bool) {
	p := exec.prm
	p.common = p.common.WithLocalOnly(false)
	p.addr.SetContainer(exec.containerID())
	p.addr.SetObject(id)

	prm := HeadPrm{
		commonPrm: p.commonPrm,
	}

	w := NewSimpleObjectWriter()
	prm.SetHeaderWriter(w)

	err := exec.svc.Head(exec.context(), prm)

	switch {
	default:
		exec.status = statusUndefined
		exec.err = err

		exec.log.Debug("could not get child object header",
			zap.Stringer("child ID", id),
			zap.String("error", err.Error()),
		)

		return nil, false
	case err == nil:
		child := w.Object()

		if !exec.isChild(child) {
			exec.status = statusUndefined

			exec.log.Debug("parent address in child object differs")
		} else {
			exec.status = statusOK
			exec.err = nil
		}

		return child, true
	}
}

func (exec execCtx) remoteClient(info clientcore.NodeInfo) (getClient, bool) {
	c, err := exec.svc.clientCache.get(info)

	switch {
	default:
		exec.status = statusUndefined
		exec.err = err

		exec.log.Debug("could not construct remote node client")
	case err == nil:
		return c, true
	}

	return nil, false
}

func mergeSplitInfo(dst, src *objectSDK.SplitInfo) {
	if last, ok := src.LastPart(); ok {
		dst.SetLastPart(last)
	}

	if link, ok := src.Link(); ok {
		dst.SetLink(link)
	}

	if splitID := src.SplitID(); splitID != nil {
		dst.SetSplitID(splitID)
	}
}

func (exec *execCtx) writeCollectedHeader() bool {
	if exec.ctxRange() != nil {
		return true
	}

	err := exec.prm.objWriter.WriteHeader(
		exec.collectedObject.CutPayload(),
	)

	switch {
	default:
		exec.status = statusUndefined
		exec.err = err

		exec.log.Debug("could not write header",
			zap.String("error", err.Error()),
		)
	case err == nil:
		exec.status = statusOK
		exec.err = nil
	}

	return exec.status == statusOK
}

func (exec *execCtx) writeObjectPayload(obj *objectSDK.Object) bool {
	if exec.headOnly() {
		return true
	}

	err := exec.prm.objWriter.WriteChunk(obj.Payload())

	switch {
	default:
		exec.status = statusUndefined
		exec.err = err

		exec.log.Debug("could not write payload chunk",
			zap.String("error", err.Error()),
		)
	case err == nil:
		exec.status = statusOK
		exec.err = nil
	}

	return err == nil
}

func (exec *execCtx) writeCollectedObject() {
	if ok := exec.writeCollectedHeader(); ok {
		exec.writeObjectPayload(exec.collectedObject)
	}
}

// isForwardingEnabled returns true if common execution
// parameters has request forwarding closure set.
func (exec execCtx) isForwardingEnabled() bool {
	return exec.prm.forwarder != nil
}

// isRangeHashForwardingEnabled returns true if common execution
// parameters has GETRANGEHASH request forwarding closure set.
func (exec execCtx) isRangeHashForwardingEnabled() bool {
	return exec.prm.rangeForwarder != nil
}

// disableForwarding removes request forwarding closure from common
// parameters, so it won't be inherited in new execution contexts.
func (exec *execCtx) disableForwarding() {
	exec.prm.SetRequestForwarder(nil)
	exec.prm.SetRangeHashRequestForwarder(nil)
}
