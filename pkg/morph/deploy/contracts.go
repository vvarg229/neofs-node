package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nspcc-dev/neo-go/pkg/core/state"
	"github.com/nspcc-dev/neo-go/pkg/core/transaction"
	"github.com/nspcc-dev/neo-go/pkg/crypto/keys"
	"github.com/nspcc-dev/neo-go/pkg/neorpc"
	"github.com/nspcc-dev/neo-go/pkg/rpcclient/actor"
	"github.com/nspcc-dev/neo-go/pkg/rpcclient/management"
	"github.com/nspcc-dev/neo-go/pkg/rpcclient/notary"
	"github.com/nspcc-dev/neo-go/pkg/smartcontract/manifest"
	"github.com/nspcc-dev/neo-go/pkg/smartcontract/nef"
	"github.com/nspcc-dev/neo-go/pkg/util"
	"github.com/nspcc-dev/neo-go/pkg/wallet"
	"go.uber.org/zap"
)

// various common methods of the NeoFS contracts.
const (
	methodUpdate = "update"
)

// syncNeoFSContractPrm groups parameters of syncNeoFSContract.
type syncNeoFSContractPrm struct {
	logger *zap.Logger

	blockchain Blockchain

	neoFS NeoFS

	// based on blockchain
	monitor *blockchainMonitor

	localAcc *wallet.Account

	// address of the NeoFS NNS contract deployed in the blockchain
	nnsContract util.Uint160
	systemEmail string

	committee keys.PublicKeys

	// with localAcc signer only
	simpleLocalActor *actor.Actor
	// committee multi-sig signs, localAcc pays
	committeeLocalActor *notary.Actor

	localNEF      nef.File
	localManifest manifest.Manifest

	// L2 domain name in domainContractAddresses TLD in the NNS
	domainName string

	// if set, syncNeoFSContract attempts to deploy the contract when it's
	// missing on the chain
	tryDeploy bool
	// is contract must be deployed by the committee
	committeeDeployRequired bool

	// optional constructor of extra arguments to be passed into method deploying
	// the contract. If returns both nil, no data is passed (noExtraDeployArgs can
	// be used).
	//
	// Ignored if tryDeploy is unset.
	buildExtraDeployArgs func() ([]interface{}, error)

	// constructor of extra arguments to be passed into method updating the
	// contract. If returns both nil, no data is passed (noExtraUpdateArgs may be
	//	used).
	buildExtraUpdateArgs func() ([]interface{}, error)

	// address of the Proxy contract deployed in the blockchain. The contract
	// pays for update transactions.
	proxyContract util.Uint160
	// set when syncNeoFSContractPrm relates to Proxy contract. In this case
	// proxyContract field is unused because address is dynamically resolved within
	// syncNeoFSContract.
	isProxy bool
}

// syncNeoFSContract behaves similar to updateNNSContract but also attempts to
// deploy the contract if it is missing on the chain and tryDeploy flag is set.
// If committeeDeployRequired is set, the contract is deployed on behalf of the
// committee with NNS custom contract scope.
//
// Returns address of the on-chain contract synchronized with the record of the
// NNS domain with parameterized name.
func syncNeoFSContract(ctx context.Context, prm syncNeoFSContractPrm) (util.Uint160, error) {
	bLocalNEF, err := prm.localNEF.Bytes()
	if err != nil {
		// not really expected
		return util.Uint160{}, fmt.Errorf("encode local NEF of the contract into binary: %w", err)
	}

	jLocalManifest, err := json.Marshal(prm.localManifest)
	if err != nil {
		// not really expected
		return util.Uint160{}, fmt.Errorf("encode local manifest of the contract into JSON: %w", err)
	}

	var proxyCommitteeActor *notary.Actor

	initProxyCommitteeActor := func(proxyContract util.Uint160) error {
		var err error
		proxyCommitteeActor, err = newProxyCommitteeNotaryActor(prm.blockchain, prm.localAcc, prm.committee, proxyContract)
		if err != nil {
			return fmt.Errorf("create Notary service client sending transactions to be signed by the committee and paid by Proxy contract: %w", err)
		}
		return nil
	}

	if !prm.isProxy {
		// otherwise, we dynamically receive Proxy contract address below and construct
		// proxyCommitteeActor after
		err = initProxyCommitteeActor(prm.proxyContract)
		if err != nil {
			return util.Uint160{}, err
		}
	}

	// wrap the parent context into the context of the current function so that
	// transaction wait routines do not leak
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var contractDeployer interface {
		Sender() util.Uint160
	}
	var managementContract *management.Contract
	if prm.committeeDeployRequired {
		deployCommitteeActor, err := newCommitteeNotaryActorWithCustomCommitteeSigner(prm.blockchain, prm.localAcc, prm.committee, func(s *transaction.Signer) {
			s.Scopes = transaction.CustomContracts
			s.AllowedContracts = []util.Uint160{prm.nnsContract}
		})
		if err != nil {
			return util.Uint160{}, fmt.Errorf("create Notary service client sending deploy transactions to be signed by the committee: %w", err)
		}

		managementContract = management.New(deployCommitteeActor)
		contractDeployer = deployCommitteeActor
	} else {
		managementContract = management.New(prm.simpleLocalActor)
		contractDeployer = prm.simpleLocalActor
	}

	var alreadyUpdated bool
	domainNameForAddress := prm.domainName + "." + domainContractAddresses
	l := prm.logger.With(zap.String("contract", prm.localManifest.Name), zap.String("domain", domainNameForAddress))
	updateTxModifier := neoFSRuntimeTransactionModifier(prm.neoFS)
	deployTxMonitor := newTransactionGroupMonitor(prm.simpleLocalActor)
	updateTxMonitor := newTransactionGroupMonitor(prm.simpleLocalActor)
	setContractRecordPrm := setNeoFSContractDomainRecordPrm{
		logger:               l,
		setRecordTxMonitor:   newTransactionGroupMonitor(prm.simpleLocalActor),
		registerTLDTxMonitor: newTransactionGroupMonitor(prm.simpleLocalActor),
		nnsContract:          prm.nnsContract,
		systemEmail:          prm.systemEmail,
		localActor:           prm.simpleLocalActor,
		committeeActor:       prm.committeeLocalActor,
		domain:               domainNameForAddress,
		record:               "", // set in for loop
	}

	for ; ; prm.monitor.waitForNextBlock(ctx) {
		select {
		case <-ctx.Done():
			return util.Uint160{}, fmt.Errorf("wait for the contract synchronization: %w", ctx.Err())
		default:
		}

		l.Info("reading on-chain state of the contract by NNS domain name...")

		var missingDomainRecord bool

		onChainState, err := readContractOnChainStateByDomainName(prm.blockchain, prm.nnsContract, domainNameForAddress)
		if err != nil {
			if errors.Is(err, neorpc.ErrUnknownContract) {
				l.Error("contract is recorded in the NNS but not found on the chain, will wait for a background fix")
				continue
			}

			missingDomainRecord = errors.Is(err, errMissingDomain) || errors.Is(err, errMissingDomainRecord)
			if !missingDomainRecord {
				if errors.Is(err, errInvalidContractDomainRecord) {
					l.Error("contract's domain record is invalid/unsupported, will wait for a background fix", zap.Error(err))
				} else {
					l.Error("failed to read on-chain state of the contract record by NNS domain name, will try again later", zap.Error(err))
				}
				continue
			}

			l.Info("could not read on-chain state of the contract by NNS domain name, trying by pre-calculated address...")

			preCalculatedAddr := state.CreateContractHash(contractDeployer.Sender(), prm.localNEF.Checksum, prm.localManifest.Name)

			onChainState, err = prm.blockchain.GetContractStateByHash(preCalculatedAddr)
			if err != nil {
				if !errors.Is(err, neorpc.ErrUnknownContract) {
					l.Error("failed to read on-chain state of the contract by pre-calculated address, will try again later",
						zap.Stringer("address", preCalculatedAddr), zap.Error(err))
					continue
				}

				onChainState = nil // for condition below, GetContractStateByHash may return empty
			}
		}

		if onChainState == nil {
			// according to instructions above, we get here when contract is missing on the chain
			if !prm.tryDeploy {
				l.Info("contract is missing on the chain but attempts to deploy are disabled, will wait for background deployment")
				continue
			}

			l.Info("contract is missing on the chain, deployment needed")

			if deployTxMonitor.isPending() {
				l.Info("previously sent transaction deploying the contract is still pending, will wait for the outcome")
				continue
			}

			extraDeployArgs, err := prm.buildExtraDeployArgs()
			if err != nil {
				l.Info("failed to prepare extra deployment arguments, will try again later", zap.Error(err))
				continue
			}

			// just to definitely avoid mutation
			nefCp := prm.localNEF
			manifestCp := prm.localManifest

			if prm.committeeDeployRequired {
				l.Info("contract requires committee witness for deployment, sending Notary request...")

				mainTxID, fallbackTxID, vub, err := prm.committeeLocalActor.Notarize(managementContract.DeployTransaction(&nefCp, &manifestCp, extraDeployArgs))
				if err != nil {
					if errors.Is(err, neorpc.ErrInsufficientFunds) {
						l.Info("insufficient Notary balance to deploy the contract, will try again later")
					} else {
						l.Error("failed to send Notary request deploying the contract, will try again later", zap.Error(err))
					}
					continue
				}

				l.Info("Notary request deploying the contract has been successfully sent, will wait for the outcome",
					zap.Stringer("main tx", mainTxID), zap.Stringer("fallback tx", fallbackTxID), zap.Uint32("vub", vub))

				deployTxMonitor.trackPendingTransactionsAsync(ctx, vub, mainTxID, fallbackTxID)

				continue
			}

			l.Info("contract does not require committee witness for deployment, sending simple transaction...")

			txID, vub, err := managementContract.Deploy(&nefCp, &manifestCp, extraDeployArgs)
			if err != nil {
				if errors.Is(err, neorpc.ErrInsufficientFunds) {
					l.Info("not enough GAS to deploy the contract, will try again later")
				} else {
					l.Error("failed to send transaction deploying the contract, will try again later", zap.Error(err))
				}
				continue
			}

			l.Info("transaction deploying the contract has been successfully sent, will wait for the outcome",
				zap.Stringer("tx", txID), zap.Uint32("vub", vub),
			)

			deployTxMonitor.trackPendingTransactionsAsync(ctx, vub, txID)

			continue
		}

		if alreadyUpdated {
			if !missingDomainRecord {
				return onChainState.Hash, nil
			}
		} else {
			extraUpdateArgs, err := prm.buildExtraUpdateArgs()
			if err != nil {
				l.Error("failed to prepare build extra arguments for the contract update, will try again later",
					zap.Error(err))
				continue
			}

			if prm.isProxy && proxyCommitteeActor == nil {
				err = initProxyCommitteeActor(onChainState.Hash)
				if err != nil {
					return util.Uint160{}, err
				}
			}

			tx, err := proxyCommitteeActor.MakeTunedCall(onChainState.Hash, methodUpdate, nil, updateTxModifier,
				bLocalNEF, jLocalManifest, extraUpdateArgs)
			if err != nil {
				if isErrContractAlreadyUpdated(err) {
					l.Info("the contract is unchanged or has already been updated")
					if !missingDomainRecord {
						return onChainState.Hash, nil
					}
					alreadyUpdated = true
				} else {
					l.Error("failed to make transaction updating the contract, will try again later", zap.Error(err))
				}
				continue
			}

			if updateTxMonitor.isPending() {
				l.Info("previously sent Notary request updating the contract is still pending, will wait for the outcome")
				continue
			}

			l.Info("sending new Notary request updating the contract...")

			mainTxID, fallbackTxID, vub, err := proxyCommitteeActor.Notarize(tx, nil)
			if err != nil {
				if errors.Is(err, neorpc.ErrInsufficientFunds) {
					l.Info("insufficient Notary balance to update the contract, will try again later")
				} else {
					l.Error("failed to send Notary request updating the contract, will try again later", zap.Error(err))
				}
				continue
			}

			l.Info("Notary request updating the contract has been successfully sent, will wait for the outcome",
				zap.Stringer("main tx", mainTxID), zap.Stringer("fallback tx", fallbackTxID), zap.Uint32("vub", vub))

			updateTxMonitor.trackPendingTransactionsAsync(ctx, vub, mainTxID, fallbackTxID)

			continue
		}

		setContractRecordPrm.record = onChainState.Hash.StringLE()

		setNeoFSContractDomainRecord(ctx, setContractRecordPrm)
	}
}
