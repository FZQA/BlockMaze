// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"crypto/ecdsa"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/zktx"
)

var CMTHashSuffix = []byte("cmt")

// StateProcessor is a basic Processor, which takes care of transitioning
// state from one point to another.
//
// StateProcessor implements Processor.
type StateProcessor struct {
	config *params.ChainConfig // Chain configuration options
	bc     *BlockChain         // Canonical block chain
	engine consensus.Engine    // Consensus engine used for block rewards
}

// NewStateProcessor initialises a new StateProcessor.
func NewStateProcessor(config *params.ChainConfig, bc *BlockChain, engine consensus.Engine) *StateProcessor {
	return &StateProcessor{
		config: config,
		bc:     bc,
		engine: engine,
	}
}

// Process processes the state changes according to the Ethereum rules by running
// the transaction messages using the statedb and applying any rewards to both
// the processor (coinbase) and any included uncles.
//
// Process returns the receipts and logs accumulated during the process and
// returns the amount of gas that was used in the process. If any of the
// transactions failed to execute due to insufficient gas it will return an error.
func (p *StateProcessor) Process(block *types.Block, statedb *state.StateDB, cfg vm.Config) (types.Receipts, []*types.Log, uint64, error) {
	var (
		receipts types.Receipts
		usedGas  = new(uint64)
		header   = block.Header()
		allLogs  []*types.Log
		gp       = new(GasPool).AddGas(block.GasLimit())
	)
	// Mutate the the block and state according to any hard-fork specs
	if p.config.DAOForkSupport && p.config.DAOForkBlock != nil && p.config.DAOForkBlock.Cmp(block.Number()) == 0 {
		misc.ApplyDAOHardFork(statedb)
	}
	// Iterate over and process the individual transactions
	for i, tx := range block.Transactions() {
		statedb.Prepare(tx.Hash(), block.Hash(), i)
		receipt, _, err := ApplyTransaction(p.config, p.bc, nil, gp, statedb, header, tx, usedGas, cfg)
		if err != nil {
			return nil, nil, 0, err
		}
		receipts = append(receipts, receipt)
		allLogs = append(allLogs, receipt.Logs...)
	}
	// Finalize the block, applying any consensus engine specific extras (e.g. block rewards)
	p.engine.Finalize(p.bc, header, statedb, block.Transactions(), block.Uncles(), receipts)

	return receipts, allLogs, *usedGas, nil
}

// ApplyTransaction attempts to apply a transaction to the given state database
// and uses the input parameters for its environment. It returns the receipt
// for the transaction, gas used and an error if the transaction failed,
// indicating the block was invalid.
func ApplyTransaction(config *params.ChainConfig, bc ChainContext, author *common.Address, gp *GasPool, statedb *state.StateDB, header *types.Header, tx *types.Transaction, usedGas *uint64, cfg vm.Config) (*types.Receipt, uint64, error) {
	msg, err := tx.AsMessage(types.MakeSigner(config, header.Number))
	if err != nil {
		return nil, 0, err
	}
	// Create a new context to be used in the EVM environment
	context := NewEVMContext(msg, header, bc, author)
	// Create a new environment which holds all relevant information
	// about the transaction and calling mechanisms.
	vmenv := vm.NewEVM(context, statedb, config, cfg)

	DB := statedb.Database()
	database := DB.TrieDB().DB()

	if tx.TxCode() == types.MintTx {
		if data, _ := database.Get(append([]byte("cmt"), tx.ZKSN().Bytes()...)); len(data) != 0 { //if sn is already exist,
			return nil, 0, errors.New("sn is already used ")
		}
		balance := statedb.GetBalance(msg.From())
		cmtbalance := statedb.GetCMTBalance(msg.From())
		if err = zktx.VerifyMintProof(&cmtbalance, tx.ZKSN(), tx.ZKCMT(), tx.ZKValue(), balance.Uint64(), tx.ZKProof()); err != nil {
			fmt.Println("invalid zkproof")
			return nil, 0, err
		}
		database.Put(append([]byte("cmt"), tx.ZKSN().Bytes()...), tx.ZKSN().Bytes())
	} else if tx.TxCode() == types.SendTx {
		if data, _ := database.Get(append([]byte("cmt"), tx.ZKSN().Bytes()...)); len(data) != 0 { //if sn is already exist,
			return nil, 0, errors.New("sn is already used ")
		}
		if err = zktx.VerifySendProof(tx.ZKSN(), tx.ZKCMT(), tx.ZKProof()); err != nil {
			fmt.Println("invalid zkproof")
			return nil, 0, err
		}
		database.Put(append([]byte("cmt"), tx.ZKSN().Bytes()...), tx.ZKSN().Bytes())
	} else if tx.TxCode() == types.UpdateTx {
		cmtbalance := statedb.GetCMTBalance(msg.From())
		if err = zktx.VerifyUpdateProof(&cmtbalance, tx.RTcmt(), tx.ZKCMT(), tx.ZKProof()); err != nil {
			fmt.Println("invalid zkproof")
			return nil, 0, err
		}
	} else if tx.TxCode() == types.DepositTx {
		if data, _ := database.Get(append([]byte("cmt"), tx.ZKSN().Bytes()...)); len(data) != 0 { //if sn is already exist,
			return nil, 0, errors.New("sn is already used ")
		}
		cmtbalance := statedb.GetCMTBalance(msg.From())
		addr1, err := types.ExtractPKBAddress(types.HomesteadSigner{}, tx) //tbd
		ppp := ecdsa.PublicKey{crypto.S256(), tx.X(), tx.Y()}
		addr2 := crypto.PubkeyToAddress(ppp)
		fmt.Println("ppp=", ppp)
		if err != nil || addr1 != addr2 {
			fmt.Println(addr1, addr2)
			return nil, 0, errors.New("invalid depositTx signature ")
		}
		if err = zktx.VerifyDepositProof(&ppp, tx.RTcmt(), &cmtbalance, tx.ZKSN(), tx.ZKCMT(), tx.ZKProof()); err != nil {
			fmt.Println("invalid zkproof")
			return nil, 0, err
		}
		database.Put(append([]byte("cmt"), tx.ZKSN().Bytes()...), tx.ZKSN().Bytes())
	} else if tx.TxCode() == types.RedeemTx {
		if data, _ := database.Get(append([]byte("cmt"), tx.ZKSN().Bytes()...)); len(data) != 0 { //if sn is already exist,
			return nil, 0, errors.New("sn is already used ")
		}
		cmtbalance := statedb.GetCMTBalance(msg.From())
		if err = zktx.VerifyRedeemProof(&cmtbalance, tx.ZKSN(), tx.ZKCMT(), tx.ZKValue(), tx.ZKProof()); err != nil {
			fmt.Println("invalid zkproof")
			return nil, 0, err
		}
		database.Put(append([]byte("cmt"), tx.ZKSN().Bytes()...), tx.ZKSN().Bytes())
	}

	// Apply the transaction to the current state (included in the env)
	_, gas, failed, err := ApplyMessage(vmenv, msg, gp)
	if err != nil {
		return nil, 0, err
	}
	if tx.TxCode() == types.SendTx {
		database.Put(append([]byte("cmtblock"), tx.ZKCMT().Bytes()...), header.Number.Bytes())
	}
	if tx.TxCode() == types.DepositTx {
		address, _ := types.ExtractPKBAddress(types.HomesteadSigner{}, tx)
		_, err = database.Get(append([]byte("randompubkeyb"), address.Bytes()...))
		if err == nil {
			return nil, 0, errors.New("cannot use randompubkey for a second time")
		}
		database.Put(append([]byte("randompubkeyb"), address.Bytes()...), address.Bytes())
	}
	// Update the state with pending changes
	var root []byte
	if config.IsByzantium(header.Number) {
		statedb.Finalise(true)
	} else {
		root = statedb.IntermediateRoot(config.IsEIP158(header.Number)).Bytes()
	}
	*usedGas += gas

	// Create a new receipt for the transaction, storing the intermediate root and gas used by the tx
	// based on the eip phase, we're passing wether the root touch-delete accounts.
	receipt := types.NewReceipt(root, failed, *usedGas)
	receipt.TxHash = tx.Hash()
	receipt.GasUsed = gas
	// if the transaction created a contract, store the creation address in the receipt.
	if msg.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(vmenv.Context.Origin, tx.Nonce())
	}
	// Set the receipt logs and create a bloom for filtering
	receipt.Logs = statedb.GetLogs(tx.Hash())
	receipt.Bloom = types.CreateBloom(types.Receipts{receipt})

	return receipt, gas, err
}
