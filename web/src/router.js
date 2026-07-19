import { createRouter, createWebHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'
import ClientsListView from './views/ClientsListView.vue'
import ClientDetailView from './views/ClientDetailView.vue'
import ClientFormView from './views/ClientFormView.vue'
import CatalogView from './views/CatalogView.vue'
import PoliciesListView from './views/PoliciesListView.vue'
import PolicyDetailView from './views/PolicyDetailView.vue'
import PolicyFormView from './views/PolicyFormView.vue'
import JobsListView from './views/JobsListView.vue'
import JobDetailView from './views/JobDetailView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: HomeView },
    { path: '/clients', component: ClientsListView },
    { path: '/clients/new', component: ClientFormView },
    { path: '/clients/:hostname', component: ClientDetailView },
    { path: '/catalog', component: CatalogView },
    { path: '/policies', component: PoliciesListView },
    { path: '/policies/new', component: PolicyFormView },
    { path: '/policies/:id', component: PolicyDetailView },
    { path: '/policies/:id/edit', component: PolicyFormView },
    { path: '/jobs', component: JobsListView },
    { path: '/jobs/:job_id', component: JobDetailView },
  ],
})
