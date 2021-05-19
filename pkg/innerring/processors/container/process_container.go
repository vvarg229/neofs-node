package container

import (
	"fmt"

	containerSDK "github.com/nspcc-dev/neofs-api-go/pkg/container"
	"github.com/nspcc-dev/neofs-node/pkg/core/container"
	containerEvent "github.com/nspcc-dev/neofs-node/pkg/morph/event/container"
	"go.uber.org/zap"
)

const (
	deleteContainerMethod = "delete"
	putContainerMethod    = "put"
)

// Process new container from the user by checking container sanity
// and sending approve tx back to morph.
func (cp *Processor) processContainerPut(put *containerEvent.Put) {
	if !cp.alphabetState.IsAlphabet() {
		cp.log.Info("non alphabet mode, ignore container put")
		return
	}

	err := cp.checkPutContainer(put)
	if err != nil {
		cp.log.Error("put container check failed",
			zap.String("error", err.Error()),
		)

		return
	}

	cp.approvePutContainer(put)
}

func (cp *Processor) checkPutContainer(e *containerEvent.Put) error {
	// unmarshal container structure
	cnr := containerSDK.New()

	err := cnr.Unmarshal(e.Container())
	if err != nil {
		return fmt.Errorf("invalid binary container: %w", err)
	}

	// perform format check
	err = container.CheckFormat(cnr)
	if err != nil {
		return fmt.Errorf("incorrect container format: %w", err)
	}

	return nil
}

func (cp *Processor) approvePutContainer(e *containerEvent.Put) {
	err := cp.morphClient.NotaryInvoke(cp.containerContract, cp.feeProvider.SideChainFee(), putContainerMethod,
		e.Container(),
		e.Signature(),
		e.PublicKey())
	if err != nil {
		cp.log.Error("could not approve put container",
			zap.String("error", err.Error()),
		)
	}
}

// Process delete container operation from the user by checking container sanity
// and sending approve tx back to morph.
func (cp *Processor) processContainerDelete(delete *containerEvent.Delete) {
	if !cp.alphabetState.IsAlphabet() {
		cp.log.Info("non alphabet mode, ignore container delete")
		return
	}

	err := cp.checkDeleteContainer(delete)
	if err != nil {
		cp.log.Error("delete container check failed",
			zap.String("error", err.Error()),
		)

		return
	}

	cp.approveDeleteContainer(delete)
}

func (cp *Processor) checkDeleteContainer(e *containerEvent.Delete) error {
	return nil
}

func (cp *Processor) approveDeleteContainer(e *containerEvent.Delete) {
	err := cp.morphClient.NotaryInvoke(cp.containerContract, cp.feeProvider.SideChainFee(), deleteContainerMethod,
		e.ContainerID(),
		e.Signature())
	if err != nil {
		cp.log.Error("could not approve delete container",
			zap.String("error", err.Error()),
		)
	}
}
