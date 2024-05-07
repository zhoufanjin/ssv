package validator

import (
	"context"
	"fmt"
	"sync"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/bloxapp/ssv-spec/qbft"
	spectypes "github.com/bloxapp/ssv-spec/types"
	"github.com/pkg/errors"
	"go.uber.org/zap"

	"github.com/bloxapp/ssv/ibft/storage"
	"github.com/bloxapp/ssv/protocol/v2/message"
	"github.com/bloxapp/ssv/protocol/v2/ssv/queue"
	"github.com/bloxapp/ssv/protocol/v2/ssv/runner"
)

type Committee struct {
	logger *zap.Logger
	ctx    context.Context

	mtx     sync.RWMutex
	Storage *storage.QBFTStores

	Queues map[phase0.Slot]queueContainer

	//runnersMtx sync.RWMutex
	Runners map[phase0.Slot]*runner.CommitteeRunner

	//sharesMtx sync.RWMutex
	Shares map[phase0.ValidatorIndex]*spectypes.Share

	Operator                *spectypes.Operator
	SignatureVerifier       spectypes.SignatureVerifier
	CreateRunnerFn          func(slot phase0.Slot, shares map[phase0.ValidatorIndex]*spectypes.Share) *runner.CommitteeRunner
	HighestAttestingSlotMap map[spectypes.ValidatorPK]phase0.Slot
}

// NewCommittee creates a new cluster
func NewCommittee(
	ctx context.Context,
	operator *spectypes.Operator,
	verifier spectypes.SignatureVerifier,
	createRunnerFn func(slot phase0.Slot, shares map[phase0.ValidatorIndex]*spectypes.Share) *runner.CommitteeRunner,
) *Committee {
	return &Committee{
		logger:                  zap.L().Named("committee"),
		ctx:                     ctx,
		Queues:                  make(map[phase0.Slot]queueContainer),
		Runners:                 make(map[phase0.Slot]*runner.CommitteeRunner),
		Shares:                  make(map[phase0.ValidatorIndex]*spectypes.Share),
		HighestAttestingSlotMap: make(map[spectypes.ValidatorPK]phase0.Slot),
		Operator:                operator,
		SignatureVerifier:       verifier,
		CreateRunnerFn:          createRunnerFn,
	}

}

func (c *Committee) AddShare(share *spectypes.Share) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.Shares[share.ValidatorIndex] = share
}

func (c *Committee) RemoveShare(validatorIndex phase0.ValidatorIndex) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	delete(c.Shares, validatorIndex)
}

// StartDuty starts a new duty for the given slot
func (c *Committee) StartDuty(logger *zap.Logger, duty *spectypes.CommitteeDuty) error {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	// TODO alan : lock per slot?
	if _, exists := c.Runners[duty.Slot]; exists {
		return errors.New(fmt.Sprintf("CommitteeRunner for slot %d already exists", duty.Slot))
	}
	//todo: mtx to get shares copy
	var sharesCopy = make(map[phase0.ValidatorIndex]*spectypes.Share, len(c.Shares))
	for k, v := range c.Shares {
		sharesCopy[k] = v
	}

	r := c.CreateRunnerFn(duty.Slot, sharesCopy)
	// Set timeout function.
	r.GetBaseRunner().TimeoutF = c.onTimeout

	c.Runners[duty.Slot] = r

	c.Queues[duty.Slot] = queueContainer{
		Q: queue.WithMetrics(queue.New(1000), nil), // TODO alan: get queue opts from options
		queueState: &queue.State{
			HasRunningInstance: false,
			Height:             qbft.Height(duty.Slot),
			Slot:               0,
			//Quorum:             options.SSVShare.Share,// TODO
		},
	}

	// TODO alan: stop queue
	go c.ConsumeQueue(logger, duty.Slot, c.ProcessMessage)

	var validatorToStopMap map[phase0.Slot]spectypes.ValidatorPK
	// Filter old duties based on highest attesting slot
	duty, validatorToStopMap, c.HighestAttestingSlotMap = FilterCommitteeDuty(duty, c.HighestAttestingSlotMap)
	// Stop validators with old duties
	c.stopDuties(validatorToStopMap)
	c.updateAttestingSlotMap(duty)
	return c.Runners[duty.Slot].StartNewDuty(logger, duty)
}

// NOT threadsafe
func (c *Committee) stopDuties(validatorToStopMap map[phase0.Slot]spectypes.ValidatorPK) {
	for slot, validator := range validatorToStopMap {
		r, exists := c.Runners[slot]
		if exists {
			r.StopDuty(validator)
		}
	}
}

func (c *Committee) PushToQueue(slot phase0.Slot, dec *queue.DecodedSSVMessage) {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	if pushed := c.Queues[slot].Q.TryPush(dec); !pushed {
		c.logger.Warn("dropping ExecuteDuty message because the queue is full")
	}
}

// FilterCommitteeDuty filters the committee duties by the slots given per validator.
// It returns the filtered duties, the validators to stop and updated slot map.
// NOT threadsafe
func FilterCommitteeDuty(duty *spectypes.CommitteeDuty, slotMap map[spectypes.ValidatorPK]phase0.Slot) (
	*spectypes.CommitteeDuty,
	map[phase0.Slot]spectypes.ValidatorPK,
	map[spectypes.ValidatorPK]phase0.Slot,
) {
	validatorsToStop := make(map[phase0.Slot]spectypes.ValidatorPK)

	for i, beaconDuty := range duty.BeaconDuties {
		validatorPK := spectypes.ValidatorPK(beaconDuty.PubKey)
		slot, exists := slotMap[validatorPK]
		if exists {
			if slot < beaconDuty.Slot {
				validatorsToStop[beaconDuty.Slot] = validatorPK
				slotMap[validatorPK] = beaconDuty.Slot
			} else { // else don't run duty with old slot
				duty.BeaconDuties[i] = nil
			}
		}
	}
	return duty, validatorsToStop, slotMap
}

// ProcessMessage processes Network Message of all types
func (c *Committee) ProcessMessage(logger *zap.Logger, msg *queue.DecodedSSVMessage) error {
	// Validate message
	if msg.GetType() != message.SSVEventMsgType {
		if err := msg.SignedSSVMessage.Validate(); err != nil {
			return errors.Wrap(err, "invalid SignedSSVMessage")
		}

		// Verify SignedSSVMessage's signature
		if err := c.SignatureVerifier.Verify(msg.SignedSSVMessage, c.Operator.Committee); err != nil {
			return errors.Wrap(err, "SignedSSVMessage has an invalid signature")
		}
	}

	switch msg.GetType() {
	case spectypes.SSVConsensusMsgType:
		qbftMsg := &qbft.Message{}
		if err := qbftMsg.Decode(msg.GetData()); err != nil {
			return errors.Wrap(err, "could not get consensus Message from network Message")
		}
		c.mtx.Lock()
		r := c.Runners[phase0.Slot(qbftMsg.Height)]
		c.mtx.Unlock()
		// TODO: check if runner is nil
		return r.ProcessConsensus(c.logger, msg.SignedSSVMessage)
	case spectypes.SSVPartialSignatureMsgType:
		pSigMessages := &spectypes.PartialSignatureMessages{}
		if err := pSigMessages.Decode(msg.SignedSSVMessage.SSVMessage.GetData()); err != nil {
			return errors.Wrap(err, "could not get post consensus Message from network Message")
		}
		if pSigMessages.Type == spectypes.PostConsensusPartialSig {
			c.mtx.Lock()
			r := c.Runners[pSigMessages.Slot]
			c.mtx.Unlock()
			// TODO: check if runner is nil
			return r.ProcessPostConsensus(c.logger, pSigMessages)
		}
	case message.SSVEventMsgType:
		return c.handleEventMessage(logger, msg)
	default:
		return errors.New("unknown msg")
	}
	return nil

}

// updateAttestingSlotMap updates the highest attesting slot map from beacon duties
func (c *Committee) updateAttestingSlotMap(duty *spectypes.CommitteeDuty) {
	for _, beaconDuty := range duty.BeaconDuties {
		if beaconDuty.Type == spectypes.BNRoleAttester {
			validatorPK := spectypes.ValidatorPK(beaconDuty.PubKey)
			if c.HighestAttestingSlotMap[validatorPK] < beaconDuty.Slot {
				c.HighestAttestingSlotMap[validatorPK] = beaconDuty.Slot
			}
		}
	}
}
