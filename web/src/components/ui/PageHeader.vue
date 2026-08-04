<!-- web/src/components/ui/PageHeader.vue -->
<script setup>
defineProps({
  title: { type: String, required: true },
  crumbs: { type: Array, default: null },
})
</script>

<template>
  <nav
    v-if="crumbs && crumbs.length"
    data-test="breadcrumb"
    aria-label="Breadcrumb"
    class="flex gap-1 text-xs text-gray-400 mb-1"
  >
    <template v-for="(crumb, index) in crumbs" :key="index">
      <router-link v-if="crumb.to" :to="crumb.to" class="hover:underline hover:text-gray-600">
        {{ crumb.label }}
      </router-link>
      <span v-else>{{ crumb.label }}</span>
      <span v-if="index < crumbs.length - 1"> / </span>
    </template>
  </nav>
  <div class="flex items-center justify-between mb-4">
    <h1 class="text-xl font-semibold">{{ title }}</h1>
    <div v-if="$slots.actions" data-test="page-header-actions" class="flex gap-2">
      <slot name="actions" />
    </div>
  </div>
  <slot />
</template>
