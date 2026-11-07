package builder_test

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ZengLLQ/polaris-relay/pkg/builder"
	"github.com/ZengLLQ/polaris-relay/pkg/state"
	"github.com/ZengLLQ/polaris-relay/pkg/types"
)

func BenchmarkBlockPacking(b *testing.B) {
	stateDB := state.NewStateDB()
	builderAddr, _ := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	proposerAddr, _ := types.HexToAddress("0xpppppppppppppppppppppppppppppppppppppppp")

	stateDB.SetAccount(&state.Account{
		Address: builderAddr,
		Balance: new(big.Int).Mul(big.NewInt(1000), big.NewInt(1e18)),
	})

	bl := builder.NewBlockBuilder("builder-bench", builderAddr, stateDB, 30000000)

	// Pre-populate mempool with 1,000 transactions
	for i := 0; i < 1000; i++ {
		senderHex := fmt.Sprintf("0x%040x", i+1)
		sender, _ := types.HexToAddress(senderHex)
		stateDB.SetAccount(&state.Account{
			Address: sender,
			Balance: new(big.Int).Mul(big.NewInt(10), big.NewInt(1e18)),
		})
		tx := types.NewTransaction(
			0,
			sender,
			builderAddr,
			big.NewInt(100),
			21000,
			big.NewInt(int64(100+i)),
			nil,
		)
		bl.SubmitTransaction(tx)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := bl.BuildOptimalBlock(
			uint64(i+1),
			types.Hash{0x01},
			proposerAddr,
			big.NewInt(100),
			big.NewInt(0),
		)
		if err != nil {
			b.Fatalf("build optimal block error: %v", err)
		}
	}
}
