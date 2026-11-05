// Static descriptions of the protocols the indexer decodes.
export const PROTOCOLS = {
  aquarius: { name: 'Aquarius', kind: 'DEX', about: 'AMM with constant-product and stableswap pools plus a multi-hop router. Trades come from pool trade(token_in, token_out, user) events.' },
  soroswap: { name: 'Soroswap', kind: 'DEX', about: 'Uniswap-v2 style pairs, router and factory. Swap tokens are inferred from transfers into and out of the pair contract.' },
  blend: { name: 'Blend', kind: 'Lending', about: 'Isolated lending pools: supply, borrow, repay, collateral moves, flash loans and liquidation auctions. amount0 is the underlying, amount1 the b/dTokens.' },
  clmm: { name: 'CLMM', kind: 'DEX', about: 'Concentrated-liquidity (Uniswap-v3 style) pools emitting swap{amount0, amount1, sqrt_price_x96} with signed amounts.' },
  amm_generic: { name: 'Generic AMM', kind: 'DEX', about: 'Other pools emitting swap{amount_in, amount_out}; tokens are inferred from transfers in the same operation.' },
};
export const protoName = (p) => PROTOCOLS[p]?.name || p;
