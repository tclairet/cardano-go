package cardano

import (
	"encoding/hex"
	"fmt"
	"github.com/echovl/ed25519"
	"github.com/fxamacker/cbor/v2"
	"github.com/tclairet/cardano-go/crypto"
	"golang.org/x/crypto/blake2b"
	"time"
)

const (
	shelleyStartTimestamp = 1596491091
	shelleyStartSlot      = 4924800
	slotMargin            = 1200

	minimumUtxoValue uint64 = 1000000
	minFeeA          uint64 = 44
	minFeeB          uint64 = 155381
)

type ProtocolParams struct {
	MinimumUtxoValue uint64
	PoolDeposit      uint64
	KeyDeposit       uint64
	MinFeeA          uint64
	MinFeeB          uint64
}

type TransactionID string

func (id TransactionID) Bytes() []byte {
	bytes, err := hex.DecodeString(string(id))
	if err != nil {
		panic(err)
	}

	return bytes
}

type Transaction struct {
	_          struct{} `cbor:",toarray"`
	Body       TransactionBody
	WitnessSet transactionWitnessSet
	Metadata   *transactionMetadata // or null
}

func (tx *Transaction) Bytes() []byte {
	bytes, err := cbor.Marshal(tx)
	if err != nil {
		panic(err)
	}
	return bytes
}

func (tx *Transaction) CborHex() string {
	return hex.EncodeToString(tx.Bytes())
}

func (tx *Transaction) ID() TransactionID {
	return tx.Body.ID()
}

func DecodeTransaction(cborHex string) (*Transaction, error) {
	bytes, err := hex.DecodeString(cborHex)
	if err != nil {
		return nil, err
	}
	tx := Transaction{}
	if err := cbor.Unmarshal(bytes, &tx); err != nil {
		return nil, err
	}
	return &tx, nil
}

func CalculateFee(tx *Transaction) uint64 {
	txBytes := tx.Bytes()
	txLength := uint64(len(txBytes))
	return minFeeA*txLength + minFeeB
}

type transactionWitnessSet struct {
	VKeyWitnessSet []vkeyWitness `cbor:"0,keyasint,omitempty"`
	// TODO: add optional fields 1-4
}

type vkeyWitness struct {
	_         struct{} `cbor:",toarray"`
	VKey      []byte   // ed25519 public key
	Signature []byte   // ed25519 signature
}

// Cbor map
type transactionMetadata map[uint64]transactionMetadatum

// This could be cbor map, array, int, bytes or a text
type transactionMetadatum struct{}

func liveTTL() uint64 {
	shelleyStart := time.Unix(shelleyStartTimestamp, 0)
	return uint64(shelleyStartSlot + time.Since(shelleyStart).Seconds() + slotMargin)
}

func NewTransactionBody(receiver Address, pickedUtxos []Utxo, amount uint64, change Address) (*TransactionBody, error) {
	return NewTransactionBodyWithTTL(
		receiver,
		pickedUtxos,
		amount,
		change, liveTTL())
}

func NewTransactionBodyWithTTL(receiver Address, pickedUtxos []Utxo, amount uint64, change Address, ttl uint64) (*TransactionBody, error) {
	var inputAmount uint64
	var inputs []TransactionInput
	for _, utxo := range pickedUtxos {
		inputs = append(inputs, TransactionInput{
			ID:    utxo.TxId.Bytes(),
			Index: utxo.Index,
		})
		inputAmount += utxo.Amount
	}

	var outputs []TransactionOutput
	outputs = append(outputs, TransactionOutput{
		Address: receiver.Bytes(),
		Amount:  amount,
	})

	transaction := &TransactionBody{
		Inputs:  inputs,
		Outputs: outputs,
		Ttl:     ttl,
	}
	if err := transaction.addFee(inputAmount, change); err != nil {
		return nil, err
	}

	return transaction, nil
}

type TransactionBody struct {
	Inputs       []TransactionInput  `cbor:"0,keyasint"`
	Outputs      []TransactionOutput `cbor:"1,keyasint"`
	Fee          uint64              `cbor:"2,keyasint"`
	Ttl          uint64              `cbor:"3,keyasint"`
	Certificates []Certificate       `cbor:"4,keyasint,omitempty"` // Omit for now
	Withdrawals  *uint               `cbor:"5,keyasint,omitempty"` // Omit for now
	Update       *uint               `cbor:"6,keyasint,omitempty"` // Omit for now
	MetadataHash *uint               `cbor:"7,keyasint,omitempty"` // Omit for now
}

func (body *TransactionBody) Bytes() []byte {
	bytes, err := cbor.Marshal(body)
	if err != nil {
		panic(err)
	}
	return bytes
}

func (body *TransactionBody) ID() TransactionID {
	hash := blake2b.Sum256(body.Bytes())
	return TransactionID(hex.EncodeToString(hash[:]))
}

func (body *TransactionBody) AddSignatures(publicKeys [][]byte, signatures [][]byte) (*Transaction, error) {
	if len(publicKeys) != len(signatures) {
		return nil, fmt.Errorf("missmatch length of publicKeys and signatures")
	}
	if len(signatures) != len(body.Inputs) {
		return nil, fmt.Errorf("missmatch length of signatures and inputs")
	}

	witnessSet := transactionWitnessSet{}

	for i := 0; i < len(publicKeys); i++ {
		if len(signatures[i]) != ed25519.SignatureSize {
			return nil, fmt.Errorf("invalid signature length %v", len(signatures[i]))
		}
		witness := vkeyWitness{VKey: publicKeys[i], Signature: signatures[i]}
		witnessSet.VKeyWitnessSet = append(witnessSet.VKeyWitnessSet, witness)
	}

	return &Transaction{
		Body:       *body,
		WitnessSet: witnessSet,
		Metadata:   nil,
	}, nil
}

func (body *TransactionBody) calculateMinFee() uint64 {
	fakeXSigningKey := crypto.NewExtendedSigningKey([]byte{
		0x0c, 0xcb, 0x74, 0xf3, 0x6b, 0x7d, 0xa1, 0x64, 0x9a, 0x81, 0x44, 0x67, 0x55, 0x22, 0xd4, 0xd8, 0x09, 0x7c, 0x64, 0x12,
	}, "")

	witnessSet := transactionWitnessSet{}
	for range body.Inputs {
		witness := vkeyWitness{VKey: fakeXSigningKey.ExtendedVerificationKey()[:32], Signature: fakeXSigningKey.Sign(fakeXSigningKey.ExtendedVerificationKey())}
		witnessSet.VKeyWitnessSet = append(witnessSet.VKeyWitnessSet, witness)
	}

	return CalculateFee(&Transaction{
		Body:       *body,
		WitnessSet: witnessSet,
		Metadata:   nil,
	})
}

func (body *TransactionBody) addFee(inputAmount uint64, changeAddress Address) error {
	// Set a temporary realistic fee in order to serialize a valid transaction
	body.Fee = 200000

	minFee := body.calculateMinFee()

	outputAmount := uint64(0)
	for _, txOut := range body.Outputs {
		outputAmount += txOut.Amount
	}
	outputWithFeeAmount := outputAmount + minFee

	if inputAmount < outputWithFeeAmount {
		return fmt.Errorf("insuficient input in transaction, got %v want atleast %v", inputAmount, outputWithFeeAmount)
	}

	if inputAmount == outputWithFeeAmount {
		body.Fee = minFee
		return nil
	}

	change := inputAmount - outputWithFeeAmount
	if change < minimumUtxoValue {
		body.Fee = minFee + change // burn change
		return nil
	}

	body.Outputs = append([]TransactionOutput{{
		Address: changeAddress.Bytes(),
		Amount:  change, // set a temporary value
	}}, body.Outputs...) // change will always be outputs[0] if present
	newMinFee := body.calculateMinFee()
	body.Outputs[0].Amount = change + minFee - newMinFee
	body.Fee = newMinFee
	return nil
}

type TransactionInput struct {
	_     struct{} `cbor:",toarray"`
	ID    []byte   // HashKey 32 bytes
	Index uint64
}

type TransactionOutput struct {
	_       struct{} `cbor:",toarray"`
	Address []byte
	Amount  uint64
}

// TODO: This should a cbor array with one element:
//  stake_registration
//	stake_deregistration
//	stake_delegation
//	pool_registration
//	pool_retirement
//	genesis_key_delegation
//	move_instantaneous_rewards_cert
type Certificate struct{}
