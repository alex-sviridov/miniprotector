<template>
  <nav class="w-48 bg-gray-100 h-screen p-4 space-y-2">
    <router-link :to="{ name: 'clients' }" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Clients
    </router-link>
    <router-link :to="{ name: 'catalog' }" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Catalog
    </router-link>
    <router-link :to="{ name: 'policies' }" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Policies
    </router-link>
    <router-link :to="{ name: 'storage' }" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Storage
    </router-link>
    <router-link :to="{ name: 'jobs' }" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Jobs
    </router-link>
  </nav>
</template>
