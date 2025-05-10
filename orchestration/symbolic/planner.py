# planner.py - Enterprise-Grade AND/OR Tree Planning System
from __future__ import annotations
import logging
from typing import Dict, List, Set, Optional, Tuple
from dataclasses import dataclass
from abc import ABC, abstractmethod
import heapq
import graphviz

logger = logging.getLogger(__name__)

@dataclass
class PlanningResult:
    sequence: List[TaskNode]
    resource_usage: Dict[str, int]
    cost: float
    risk_factor: float

class PlanException(Exception):
    """Base exception for planning failures"""
    pass

class CircularDependencyError(PlanException):
    """Raised when cyclic dependencies detected"""
    pass

class ResourceConflictError(PlanException):
    """Raised when resource constraints violated"""
    pass

class TaskNode(ABC):
    def __init__(self, 
                 task_id: str,
                 preconditions: List[TaskNode] = None,
                 resources: Dict[str, int] = None,
                 cost: float = 0,
                 risk: float = 0):
        self.id = task_id
        self.preconditions = preconditions or []
        self.resources = resources or {}
        self.cost = cost
        self.risk = risk
        self._state: str = "PENDING"
        self._children: List[TaskNode] = []
        
    @abstractmethod
    def is_atomic(self) -> bool:
        """Determine if task cannot be decomposed further"""
        pass
    
    @property
    def state(self) -> str:
        return self._state
    
    def add_child(self, node: TaskNode):
        """Add subtask dependency"""
        if node in self.get_ancestors():
            raise CircularDependencyError(f"Circular reference detected adding {node.id} to {self.id}")
        self._children.append(node)
        
    def get_ancestors(self) -> Set[TaskNode]:
        """Get all parent dependencies"""
        ancestors = set()
        stack = [self]
        while stack:
            current = stack.pop()
            for parent in current.preconditions:
                if parent not in ancestors:
                    ancestors.add(parent)
                    stack.append(parent)
        return ancestors
    
    def validate_dag(self):
        """Verify no cyclic dependencies in subtree"""
        visited = set()
        stack = [(self, set())]
        while stack:
            node, path = stack.pop()
            if node in path:
                raise CircularDependencyError(f"Cycle detected at {node.id}")
            if node not in visited:
                visited.add(node)
                new_path = path | {node}
                for child in node._children:
                    stack.append((child, new_path))

class ANDNode(TaskNode):
    def is_atomic(self) -> bool:
        return False
    
    def decompose(self, 
                  available_resources: Dict[str, int]
                ) -> List[List[TaskNode]]:
        """Generate all valid decomposition paths"""
        options = []
        for child in self._children:
            try:
                child.validate_resources(available_resources)
                options.append(child)
            except ResourceConflictError:
                continue
        return [options] if options else []

class ORNode(TaskNode):
    def is_atomic(self) -> bool:
        return False
    
    def decompose(self,
                  available_resources: Dict[str, int]
                ) -> List[List[TaskNode]]:
        """Generate alternative decomposition paths"""
        alternatives = []
        for child in self._children:
            try:
                child.validate_resources(available_resources)
                alternatives.append([child])
            except ResourceConflictError:
                continue
        return alternatives

class AtomicNode(TaskNode):
    def is_atomic(self) -> bool:
        return True

class PlanningGraph:
    def __init__(self,
                 root: TaskNode,
                 resource_pool: Dict[str, int],
                 risk_threshold: float = 0.7):
        self.root = root
        self.resource_pool = resource_pool.copy()
        self.risk_threshold = risk_threshold
        self._cache: Dict[str, PlanningResult] = {}
        
    def generate_plan(self) -> PlanningResult:
        """Generate optimal plan using AO* algorithm"""
        try:
            self.root.validate_dag()
            return self._ao_star_search()
        except CircularDependencyError as e:
            logger.error(f"Planning aborted: {str(e)}")
            raise
    
    def _ao_star_search(self) -> PlanningResult:
        heap = []
        heapq.heappush(heap, (0, self.root))
        best_plan = None
        
        while heap:
            current_cost, current_node = heapq.heappop(heap)
            
            if current_node.is_atomic():
                continue
                
            decompositions = current_node.decompose(self.resource_pool)
            
            for option in decompositions:
                try:
                    plan = self._evaluate_option(option)
                    if plan.risk_factor > self.risk_threshold:
                        continue
                        
                    total_cost = current_cost + plan.cost
                    if not best_plan or total_cost < best_plan.cost:
                        best_plan = plan
                        best_plan.cost = total_cost
                        heapq.heappush(heap, (total_cost, option[-1]))
                        
                except ResourceConflictError:
                    continue
                    
        if not best_plan:
            raise PlanException("No valid plan found within constraints")
        return best_plan
    
    def _evaluate_option(self, 
                       nodes: List[TaskNode]
                     ) -> PlanningResult:
        """Evaluate resource usage and risks for a decomposition path"""
        required_resources = {}
        total_cost = 0
        total_risk = 0
        
        for node in nodes:
            if node.state != "PENDING":
                continue
                
            for res, amount in node.resources.items():
                required_resources[res] = required_resources.get(res, 0) + amount
                if required_resources[res] > self.resource_pool.get(res, 0):
                    raise ResourceConflictError(
                        f"Insufficient {res}: {required_resources[res]}/{self.resource_pool[res]}"
                    )
                    
            total_cost += node.cost
            total_risk = max(total_risk, node.risk)
            
        return PlanningResult(
            sequence=nodes,
            resource_usage=required_resources,
            cost=total_cost,
            risk_factor=total_risk
        )

    def visualize(self, filename: str = "plan"):
        """Generate DOT graph visualization"""
        dot = graphviz.Digraph()
        stack = [self.root]
        visited = set()
        
        while stack:
            node = stack.pop()
            if node in visited:
                continue
            visited.add(node)
            
            dot.node(node.id, 
                    shape='rectangle' if isinstance(node, ANDNode) else 'diamond',
                    style='filled' if node.is_atomic() else '',
                    fillcolor='lightgrey' if node.is_atomic() else 'white')
            
            for child in node._children:
                dot.edge(node.id, child.id)
                stack.append(child)
                
        dot.render(filename, format='png', cleanup=True)

# Example Usage
if __name__ == "__main__":
    # Define resource pool
    resources = {
        "cpu": 8,
        "memory": 16384,  # MB
        "gpu": 1,
        "quantum_secure": True
    }
    
    # Build task hierarchy
    root = ANDNode("PlanMission", resources={"cpu": 2})
    
    acquire_data = ORNode("AcquireData", resources={"memory": 4096})
    root.add_child(acquire_data)
    
    # Data acquisition options
    sensor = AtomicNode("SensorData", 
                       resources={"cpu": 1, "memory": 2048},
                       cost=50, risk=0.2)
    api = AtomicNode("APIIngest", 
                    resources={"cpu": 2, "memory": 1024},
                    cost=30, risk=0.4)
    acquire_data.add_child(sensor)
    acquire_data.add_child(api)
    
    process = ANDNode("ProcessData", resources={"cpu": 4, "gpu": 1})
    root.add_child(process)
    
    # Processing steps
    clean = AtomicNode("CleanData", resources={"memory": 512}, cost=10)
    transform = AtomicNode("Transform", resources={"cpu": 2}, cost=20)
    process.add_child(clean)
    process.add_child(transform)
    
    # Execute planning
    try:
        planner = PlanningGraph(root, resources)
        plan = planner.generate_plan()
        print(f"Optimal plan cost: {plan.cost}, Risk: {plan.risk_factor}")
        print("Execution sequence:")
        for task in plan.sequence:
            print(f"- {task.id}")
        planner.visualize()
        
    except PlanException as e:
        print(f"Planning failed: {str(e)}")
