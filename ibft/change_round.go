package ibft

import (
	"bytes"
	"encoding/json"
	"errors"

	"go.uber.org/zap"

	"github.com/bloxapp/ssv/ibft/types"
)

//type roundChangeData struct {
//	PreparedRound        uint64
//	PreparedValue        []byte
//	justificationMessage *types.Message
//	justificationSig     []byte
//}

func (i *iBFTInstance) roundChangeInputValue() ([]byte, error) {
	data := &types.ChangeRoundData{
		PreparedRound: i.state.PreparedRound,
		PreparedValue: i.state.PreparedValue,
	}

	return json.Marshal(data)
}

/**
### Algorithm 4 IBFT pseudocode for process pi: message justification
	Helper function that returns a tuple (pr, pv) where pr and pv are, respectively,
	the prepared round and the prepared value of the ROUND-CHANGE message in Qrc with the highest prepared round.
	function HighestPrepared(Qrc)
		return (pr, pv) such that:
			∃⟨ROUND-CHANGE, λi, round, pr, pv⟩ ∈ Qrc :
				∀⟨ROUND-CHANGE, λi, round, prj, pvj⟩ ∈ Qrc : prj = ⊥ ∨ pr ≥ prj
*/
func (i *iBFTInstance) highestPrepared(round uint64) (changeData *types.ChangeRoundData, err error) {
	for _, msg := range i.roundChangeMessages.ReadOnlyMessagesByRound(round) {
		if msg.Message.Value == nil {
			continue
		}
		candidateChangeData := &types.ChangeRoundData{}
		err = json.Unmarshal(msg.Message.Value, candidateChangeData)
		if err != nil {
			return nil, err
		}

		// compare to highest found
		if changeData != nil {
			if candidateChangeData.PreparedRound > changeData.PreparedRound {
				changeData = candidateChangeData
			}
		} else {
			changeData = candidateChangeData
		}
	}
	return changeData, nil
}

/**
### Algorithm 4 IBFT pseudocode for process pi: message justification
predicate JustifyRoundChange(Qrc) return
	∀⟨ROUND-CHANGE, λi, ri, prj, pvj⟩ ∈ Qrc : prj = ⊥ ∧ pvj = ⊥
	∨ received a quorum of valid ⟨PREPARE, λi, pr, pv⟩ messages such that:
		(pr, pv) = HighestPrepared(Qrc)
*/
func (i *iBFTInstance) justifyRoundChange(round uint64) (bool, error) {
	cnt := 0
	// Find quorum for round change messages with prj = ⊥ ∧ pvj = ⊥
	for _, msg := range i.roundChangeMessages.ReadOnlyMessagesByRound(round) {
		if msg.Message.Value == nil {
			cnt++
		}
	}

	quorum := cnt*3 >= i.params.CommitteeSize()*2
	if quorum { // quorum for prj = ⊥ ∧ pvj = ⊥ found
		return true, nil
	} else {
		data, err := i.highestPrepared(round)
		if err != nil {
			return false, err
		}
		if data == nil {
			return false, errors.New("could not justify round change, did not find highest prepared")
		}
		if !i.state.PreviouslyPrepared() { // no previous prepared round
			return false, errors.New("could not justify round change, did not received quorum of prepare messages previously")
		}
		return data.PreparedRound == i.state.PreparedRound &&
			bytes.Equal(data.PreparedValue, i.state.PreparedValue), nil
	}
}

func (i *iBFTInstance) validateRoundChange(msg *types.SignedMessage) error {
	// TODO - round change with prepared_round/ prepared_value should also carry justifications
	// TODO - verify justification sigs

	if err := i.implementation.ValidatePrepareMsg(i.state, msg); err != nil {
		return err
	}

	return nil
}

func (i *iBFTInstance) validateChangeRoundMsg(msg *types.Message) error {
	// msg.value holds the justification value in a change round message
	if msg.Value != nil {
		if msg.Type != types.RoundState_ChangeRound {
			return errors.New("msg not a change round message")
		}
		if msg.Value == nil {
			return errors.New("change round data is nil")
		}

		data := &types.ChangeRoundData{}
		if err := json.Unmarshal(msg.Value, data); err != nil {
			return err
		}
		if data.PreparedRound != data.JustificationMsg.Round {
			return errors.New("change round prepared round not equal to justification msg round")
		}
		if !bytes.Equal(data.PreparedValue, data.JustificationMsg.Value) {
			return errors.New("change round prepared value not equal to justification msg value")
		}

		// validate signature
		pks, err := i.params.PubKeysById(data.SignedIds)
		if err != nil {
			return err
		}
		aggregated := types.PubKeys(pks).Aggregate()
		res, err := data.VerifySig(aggregated)
		if err != nil {
			return err
		}
		if !res {
			return errors.New("change round justification signature doesn't verify")
		}

	}
	return nil
}

func (i *iBFTInstance) uponChangeRoundTrigger() {
	i.log.Info("round timeout, changing round", zap.Uint64("round", i.state.Round))

	// bump round
	i.state.Round++

	// set time for next round change
	i.triggerRoundChangeOnTimer()

	// broadcast round change
	data, err := i.roundChangeInputValue()
	if err != nil {
		i.log.Error("failed to create round change data for round", zap.Uint64("round", i.state.Round), zap.Error(err))
	}
	broadcastMsg := &types.Message{
		Type:   types.RoundState_ChangeRound,
		Round:  i.state.Round,
		Lambda: i.state.Lambda,
		Value:  data,
	}
	if err := i.network.Broadcast(broadcastMsg); err != nil {
		i.log.Error("could not broadcast round change message", zap.Error(err))
	}
}

func (i *iBFTInstance) uponChangeRoundMessage(msg *types.SignedMessage) {
	i.log.Info("changing round")
}