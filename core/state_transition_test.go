package core

import (
	"bytes"
	"crypto/ecdsa"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/mint"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/assert"
	"math"
	"math/big"
	"testing"
)

var ownerKey, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
var ownerAddr = crypto.PubkeyToAddress(ownerKey.PublicKey)
var mintLimit = new(big.Int).Mul(big.NewInt(1000), big.NewInt(params.Ether))

func prepareStateDb() *state.StateDB {
	stateDb, _ := state.New(common.Hash{}, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)

	stateDb.SetCode(mint.Contract.Address, mint.Contract.Bytecode)
	stateDb.SetState(mint.Contract.Address, mint.Contract.StorageLayout.Owner, ownerAddr.Hash())
	stateDb.SetState(mint.Contract.Address, mint.Contract.StorageLayout.MintLimit, common.BigToHash(mintLimit))
	stateDb.AddBalance(ownerAddr, big.NewInt(params.Ether))
	stateDb.Finalise(true)

	return stateDb
}

func TestIncorrectMintInstruction(t *testing.T) {
	sender2Key, _ := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f292")
	sender2Addr := crypto.PubkeyToAddress(sender2Key.PublicKey)

	blockNum := big.NewInt(100)
	blockCtx := vm.BlockContext{
		CanTransfer: func(db vm.StateDB, address common.Address, b *big.Int) bool { return true },
		Transfer:    func(db vm.StateDB, address common.Address, address2 common.Address, b *big.Int) {},
		BlockNumber: blockNum,
	}

	chainConfig := params.AllEthashProtocolChanges
	chainConfig.LondonBlock = nil

	validMintAmount := new(big.Int).Mul(big.NewInt(500), big.NewInt(params.Ether))
	validData := bytes.Join([][]byte{
		common.BigToHash(validMintAmount).Bytes(),
		(common.Hash{}).Bytes(),
		{byte(1)},
	}, []byte{})

	testCases := []struct {
		signerKey     *ecdsa.PrivateKey
		tx            *types.LegacyTx
		expectedError string
		modifyStateDb func(stateDb *state.StateDB)
	}{
		{
			signerKey: ownerKey,
			tx: &types.LegacyTx{
				Nonce:    0,
				To:       &mint.Contract.Address,
				Value:    new(big.Int),
				Gas:      100000,
				Data:     bytes.Repeat([]byte{2}, 65),
				GasPrice: big.NewInt(params.GWei),
			},
			expectedError: "invalid burn tx network in mint instruction",
		},
		{
			signerKey: sender2Key,
			tx: &types.LegacyTx{
				Nonce:    0,
				To:       &mint.Contract.Address,
				Value:    new(big.Int),
				Gas:      100000,
				Data:     bytes.Repeat([]byte{0}, 65),
				GasPrice: big.NewInt(params.GWei),
			},
			expectedError: "transaction sender is not allowed to mint",
			modifyStateDb: func(stateDb *state.StateDB) {
				stateDb.AddBalance(sender2Addr, big.NewInt(params.Ether))
				stateDb.Finalise(true)
			},
		},
		{
			signerKey: ownerKey,
			tx: &types.LegacyTx{
				Nonce:    0,
				To:       &mint.Contract.Address,
				Value:    new(big.Int),
				Gas:      100000,
				Data:     bytes.Repeat([]byte{1}, 65),
				GasPrice: big.NewInt(params.GWei),
			},
			expectedError: "mint amount exceeds mint limit",
		},
		{
			signerKey: ownerKey,
			tx: &types.LegacyTx{
				Nonce:    0,
				To:       &mint.Contract.Address,
				Value:    new(big.Int),
				Gas:      100000,
				Data:     validData,
				GasPrice: big.NewInt(params.GWei),
			},
			expectedError: "mint contract not found in current state",
			modifyStateDb: func(stateDb *state.StateDB) {
				stateDb.SetCode(mint.Contract.Address, mint.Contract.Bytecode[:len(mint.Contract.Bytecode)-106])
				stateDb.Finalise(true)
			},
		},
	}

	signer := types.HomesteadSigner{}

	for _, testCase := range testCases {
		t.Run(testCase.expectedError, func(t *testing.T) {
			logRecords := make([]*log.Record, 0)
			log.Root().SetHandler(log.FuncHandler(func(r *log.Record) error { logRecords = append(logRecords, r); return nil }))

			stateDb := prepareStateDb()
			if testCase.modifyStateDb != nil {
				testCase.modifyStateDb(stateDb)
			}

			evm := vm.NewEVM(blockCtx, vm.TxContext{}, stateDb, chainConfig, vm.Config{NoBaseFee: true})

			tx, _ := types.SignNewTx(testCase.signerKey, signer, testCase.tx)
			message, _ := tx.AsMessage(signer, nil)
			result, err := ApplyMessage(evm, message, new(GasPool).AddGas(math.MaxUint64))

			successful := assert.NoError(t, err) &&
				assert.ErrorIs(t, result.Err, vm.ErrExecutionReverted) &&
				assert.Len(t, logRecords, 1) &&
				assert.Equal(t, testCase.expectedError, logRecords[0].Msg)

			if !successful {
				t.Error("test failed")
			}
		})
	}
}

func TestSuccessfulMint(t *testing.T) {
	stateDb := prepareStateDb()

	blockNum := big.NewInt(100)
	blockCtx := vm.BlockContext{
		CanTransfer: func(db vm.StateDB, address common.Address, b *big.Int) bool { return true },
		Transfer:    func(db vm.StateDB, address common.Address, address2 common.Address, b *big.Int) {},
		BlockNumber: blockNum,
	}

	chainConfig := params.AllCliqueProtocolChanges
	chainConfig.LondonBlock = nil

	mintAmount := new(big.Int).Mul(big.NewInt(500), big.NewInt(params.Ether))
	burnTxHash := common.HexToHash("0x621c759718a44e19ad04f8d133746b1043a2004f3fd68028cd28f1598388106e")
	burnTxNetwork := byte(0)

	data := bytes.Join([][]byte{common.BigToHash(mintAmount).Bytes(), burnTxHash.Bytes(), {burnTxNetwork}}, []byte{})

	evm := vm.NewEVM(blockCtx, vm.TxContext{}, stateDb, chainConfig, vm.Config{NoBaseFee: true})
	signer := types.HomesteadSigner{}

	tx, _ := types.SignNewTx(ownerKey, signer, &types.LegacyTx{
		Nonce:    0,
		To:       &mint.Contract.Address,
		Value:    new(big.Int),
		Gas:      100000,
		Data:     data,
		GasPrice: big.NewInt(params.GWei),
	})

	message, _ := tx.AsMessage(signer, nil)

	result, err := ApplyMessage(evm, message, new(GasPool).AddGas(math.MaxUint64))

	assert.NoError(t, err)
	assert.NoError(t, result.Err)
	assert.Equal(t, uint64(21716), result.UsedGas)

	// Fee = 21716 * 1 gwei
	// Mint amount = 500 * 1 ether
	// Previous balance = 1 ether
	// Expected balance = Previous balance - Fee + Mint amount = 500.999978284 ether
	expectedBalance, _ := new(big.Int).SetString("500999978284000000000", 10)
	// Previous mint limit = 1000
	// Expected mint limit = Previous mint limit - Mint amount
	expectedMintLimit := common.BigToHash(new(big.Int).Mul(big.NewInt(500), big.NewInt(params.Ether)))

	assert.Equal(t, uint64(1), stateDb.GetNonce(ownerAddr))
	assert.Equal(t, expectedBalance, stateDb.GetBalance(ownerAddr))
	assert.Equal(t, expectedMintLimit, stateDb.GetState(mint.Contract.Address, mint.Contract.StorageLayout.MintLimit))

	assert.Len(t, stateDb.Logs(), 1)
	assert.Equal(t, &types.Log{
		Address: mint.Contract.Address,
		Topics:  []common.Hash{common.HexToHash("0d9811f14a9cfa628d4819902adcdd4ff09f73ac9c2628280058dc2146fa247d")},
		Data: bytes.Join([][]byte{
			common.BigToHash(mintAmount).Bytes(),
			burnTxHash.Bytes(),
			common.BytesToHash([]byte{burnTxNetwork}).Bytes(),
		}, []byte{}),
		BlockNumber: blockNum.Uint64(),
	}, stateDb.Logs()[0])
}
