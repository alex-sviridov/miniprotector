<script setup>
import { ref } from 'vue'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const input = ref('')

function submit() {
  if (input.value.trim()) {
    auth.setToken(input.value.trim())
    input.value = ''
  }
}
</script>

<template>
  <div v-if="!auth.isAuthenticated" class="fixed inset-0 flex items-center justify-center bg-gray-900/80">
    <form @submit.prevent="submit" class="bg-white p-6 rounded shadow w-80 space-y-3">
      <h2 class="text-lg font-semibold">Enter API token</h2>
      <input
        v-model="input"
        type="password"
        placeholder="Bearer token"
        class="w-full border rounded px-2 py-1"
      />
      <button type="submit" class="w-full bg-blue-600 text-white rounded py-1">Continue</button>
    </form>
  </div>
</template>
