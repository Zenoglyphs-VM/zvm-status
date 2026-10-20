<script setup lang="ts">
// How to point a wallet at this stack. People reached the status page asking
// "is it up?" and then "what do I put in Rabby?", and nothing on the site said.
// The values are the build's own (see .env.example), the same ones every app in
// the stack is pointed at, so this page lists exactly what they read.
//
// On a phone each field is its label over its value: beside a 8.5rem label
// column, a URL had about eighteen characters of room and broke mid-word.
import {computed, onMounted, ref} from 'vue'
import Icon from './Icon.vue'
import {askChain, nomChain, rpc, zvmChain as CHAIN_ID} from '../chain'

const gateway = ((import.meta.env.VITE_GATEWAY as string | undefined) || location.origin).replace(/\/+$/, '')
const CHAIN_NAME = 'ZVM devnet'
const explorer = `${gateway}/explorer/`
// Syrius accepts ws:// and wss:// only; it refuses an https node URL outright.
const zenon = import.meta.env.VITE_NOM_NODE as string
onMounted(askChain)

const evm = computed(() => [
  {label: 'Network name', value: CHAIN_NAME},
  {label: 'RPC URL', value: rpc},
  {label: 'Chain ID', value: String(CHAIN_ID.value)},
  {label: 'Currency symbol', value: 'ZNN'},
  {label: 'Block explorer', value: explorer},
])
const syrius = computed(() => [
  {label: 'Node URL', value: zenon},
  {label: 'Chain identifier', value: String(nomChain.value)},
])

const copied = ref('')
async function copy(value: string) {
  try {
    await navigator.clipboard.writeText(value)
    copied.value = value
    setTimeout(() => copied.value === value && (copied.value = ''), 1500)
  } catch {
    /* clipboard needs a secure context; the value is selectable text anyway */
  }
}

// One click instead of five fields, for a wallet that injects window.ethereum.
// Switch first: a wallet that already has the chain answers 4902 only when it
// does not, and some nest that code.
const eth = (window as any).ethereum
const adding = ref(false)
const addResult = ref<{ok: boolean; text: string} | null>(null)
async function addToWallet() {
  adding.value = true
  addResult.value = null
  const chainId = `0x${CHAIN_ID.value.toString(16)}`
  try {
    try {
      await eth.request({method: 'wallet_switchEthereumChain', params: [{chainId}]})
    } catch (err: any) {
      const code = err?.code ?? err?.data?.originalError?.code
      if (code !== 4902 && code !== -32603) throw err
      await eth.request({
        method: 'wallet_addEthereumChain',
        params: [{chainId, chainName: CHAIN_NAME, nativeCurrency: {name: 'Zenon', symbol: 'ZNN', decimals: 18}, rpcUrls: [rpc], blockExplorerUrls: [explorer]}],
      })
    }
    addResult.value = {ok: true, text: `Wallet is on ${CHAIN_NAME}`}
  } catch (err: any) {
    addResult.value = {ok: false, text: err?.code === 4001 ? 'Cancelled in the wallet' : (err?.message ?? 'The wallet refused')}
  } finally {
    adding.value = false
  }
}
</script>

<template>
  <section id="connect" class="panel rail-in scroll-mt-16" style="animation-delay: 320ms">
    <div class="px-4 py-2.5 border-b border-line flex items-center">
      <span class="ledger">Connect a wallet</span>
    </div>

    <div class="grid md:grid-cols-2">
      <div class="px-4 py-4 border-b md:border-b-0 md:border-r border-line grid gap-3 content-start">
        <div>
          <h2 class="font-medium text-[14.5px]">Rabby or MetaMask</h2>
          <p class="text-[12.5px] text-dim mt-0.5">The EVM side: ZVM contracts, glyphswap, karum.</p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button v-if="eth" class="btn btn-sm" :disabled="adding" @click="addToWallet">
            <Icon name="plus" :size="14" />
            {{ adding ? 'Check your wallet…' : 'Add to wallet' }}
          </button>
          <span v-if="addResult" class="text-[12px] flex items-center gap-1" :style="{color: addResult.ok ? 'var(--ok)' : 'var(--danger)'}">
            <Icon :name="addResult.ok ? 'check' : 'alert'" :size="13" />
            {{ addResult.text }}
          </span>
        </div>
        <dl class="grid gap-1 sm:gap-1.5">
          <div v-for="f in evm" :key="f.label" class="grid grid-cols-[minmax(0,1fr)_auto] sm:grid-cols-[8.5rem_minmax(0,1fr)_auto] items-center gap-x-2">
            <dt class="col-span-2 sm:col-span-1 text-[12px] text-dim">{{ f.label }}</dt>
            <dd class="mono text-[12.5px] [overflow-wrap:anywhere]">{{ f.value }}</dd>
            <button class="btn btn-sm btn-quiet px-1.5" :title="`Copy ${f.label.toLowerCase()}`" @click="copy(f.value)">
              <Icon :name="copied === f.value ? 'check' : 'copy'" :size="13" />
              <span class="sr-only">Copy {{ f.label }}</span>
            </button>
          </div>
        </dl>
        <p class="text-[12px] text-dim">
          By hand: Rabby → More → Custom Network → Add; MetaMask → Networks → Add a custom network.
        </p>
      </div>

      <div class="px-4 py-4 grid gap-3 content-start">
        <div>
          <h2 class="font-medium text-[14.5px]">Syrius extension</h2>
          <p class="text-[12.5px] text-dim mt-0.5">The Zenon side: the NoM chain this rollup is based on.</p>
        </div>
        <dl class="grid gap-1 sm:gap-1.5">
          <div v-for="f in syrius" :key="f.label" class="grid grid-cols-[minmax(0,1fr)_auto] sm:grid-cols-[8.5rem_minmax(0,1fr)_auto] items-center gap-x-2">
            <dt class="col-span-2 sm:col-span-1 text-[12px] text-dim">{{ f.label }}</dt>
            <dd class="mono text-[12.5px] [overflow-wrap:anywhere]">{{ f.value }}</dd>
            <button class="btn btn-sm btn-quiet px-1.5" :title="`Copy ${f.label.toLowerCase()}`" @click="copy(f.value)">
              <Icon :name="copied === f.value ? 'check' : 'copy'" :size="13" />
              <span class="sr-only">Copy {{ f.label }}</span>
            </button>
          </div>
        </dl>
        <ol class="text-[12.5px] text-soft grid gap-1 list-decimal pl-4">
          <li>Open the extension → Settings → Node.</li>
          <li>Paste the node URL, add it, and select it.</li>
          <li>Under Chain identifier, press <span class="text-fg">Use chain {{ nomChain }}</span>. Until the wallet signs for {{ nomChain }}, every block it sends is rejected.</li>
        </ol>
      </div>
    </div>

    <p class="px-4 py-2.5 border-t border-line text-[12px] text-dim">
      Need funds? The <a class="link" :href="`${gateway}/glyphswap/faucet`">faucet</a> takes either a z1 or a 0x address.
    </p>
  </section>
</template>
