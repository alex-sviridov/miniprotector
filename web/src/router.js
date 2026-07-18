import { createRouter, createWebHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'
import ClientsListView from './views/ClientsListView.vue'
import ClientDetailView from './views/ClientDetailView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: HomeView },
    { path: '/clients', component: ClientsListView },
    { path: '/clients/:hostname', component: ClientDetailView },
  ],
})
