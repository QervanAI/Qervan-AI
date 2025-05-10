# dqn.py - Enterprise Reinforcement Learning System
import torch
import torch.nn as nn
import torch.optim as optim
import numpy as np
import random
from collections import deque
from typing import Deque, Tuple, Dict, Optional
import logging
import json
import os
from datetime import datetime
import matplotlib.pyplot as plt
from tensorboardX import SummaryWriter
from omegaconf import OmegaConf

# Configuration Management
class DQNConfig:
    def __init__(self):
        self.config = OmegaConf.create({
            'model': {
                'state_dim': 24,
                'action_dim': 4,
                'hidden_layers': [256, 128],
                'dueling': True,
                'double_dqn': True
            },
            'training': {
                'buffer_size': int(1e6),
                'batch_size': 1024,
                'gamma': 0.99,
                'tau': 1e-3,
                'lr': 5e-4,
                'update_every': 4,
                'pretrain_steps': 1024,
                'num_episodes': 2000,
                'max_steps': 1000,
                'epsilon': {
                    'start': 1.0,
                    'end': 0.01,
                    'decay': 0.995
                }
            },
            'system': {
                'mixed_precision': True,
                'prioritized_replay': True,
                'n_step': 3,
                'distributional': True,
                'num_atoms': 51
            }
        })

# Neural Architecture
class QuantumDuelingDQN(nn.Module):
    def __init__(self, config: OmegaConf):
        super().__init__()
        self.num_atoms = config.system.num_atoms
        
        # Feature Extraction
        self.feature = nn.Sequential(
            nn.Linear(config.model.state_dim, config.model.hidden_layers[0]),
            nn.GELU(),
            nn.LayerNorm(config.model.hidden_layers[0])
        )
        
        # Value Stream
        self.value_stream = nn.Sequential(
            nn.Linear(config.model.hidden_layers[0], config.model.hidden_layers[1]),
            nn.GELU(),
            nn.Linear(config.model.hidden_layers[1], self.num_atoms)
        )
        
        # Advantage Stream 
        self.advantage_stream = nn.Sequential(
            nn.Linear(config.model.hidden_layers[0], config.model.hidden_layers[1]),
            nn.GELU(),
            nn.Linear(config.model.hidden_layers[1], config.model.action_dim * self.num_atoms)
        )

    def forward(self, state: torch.Tensor) -> torch.Tensor:
        features = self.feature(state)
        values = self.value_stream(features).view(-1, 1, self.num_atoms)
        advantages = self.advantage_stream(features).view(-1, config.model.action_dim, self.num_atoms)
        q_dist = values + (advantages - advantages.mean(dim=1, keepdim=True))
        return nn.Softmax(dim=2)(q_dist)

# Experience Replay System
class PrioritizedReplayBuffer:
    def __init__(self, buffer_size: int, batch_size: int, alpha: float = 0.6):
        self.buffer_size = buffer_size
        self.batch_size = batch_size
        self.alpha = alpha
        self.buffer = deque(maxlen=buffer_size)
        self.priorities = deque(maxlen=buffer_size)
        self.pos = 0
        self._max_priority = 1.0

    def add(self, experience: Tuple) -> None:
        self.buffer.append(experience)
        self.priorities.append(self._max_priority ** self.alpha)
        
    def sample(self) -> Tuple:
        probs = np.array(self.priorities) ** self.alpha
        probs /= probs.sum()
        indices = np.random.choice(len(self.buffer), self.batch_size, p=probs)
        experiences = [self.buffer[idx] for idx in indices]
        
        # Calculate importance sampling weights
        weights = (len(self.buffer) * probs[indices]) ** (-beta)
        weights /= weights.max()
        
        return experiences, indices, weights
        
    def update_priorities(self, indices: list, errors: np.ndarray) -> None:
        for idx, error in zip(indices, errors):
            self.priorities[idx] = (abs(error) + 1e-8) ** self.alpha

# Core Agent Implementation
class EnterpriseDQNAgent:
    def __init__(self, config: DQNConfig):
        self.config = config.config
        self.device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
        
        # Initialize Q-networks
        self.qnetwork_local = QuantumDuelingDQN(self.config.model).to(self.device)
        self.qnetwork_target = QuantumDuelingDQN(self.config.model).to(self.device)
        self.optimizer = optim.AdamW(self.qnetwork_local.parameters(), 
                                   lr=self.config.training.lr)
        
        # Replay buffer
        self.memory = PrioritizedReplayBuffer(
            self.config.training.buffer_size,
            self.config.training.batch_size
        )
        
        # Training state
        self.t_step = 0
        self.epsilon = self.config.training.epsilon.start
        self.writer = SummaryWriter()
        self.scaler = torch.cuda.amp.GradScaler(
            enabled=self.config.system.mixed_precision
        )
        
    def step(self, state: np.ndarray, action: int, reward: float, 
            next_state: np.ndarray, done: bool) -> None:
        self.memory.add((state, action, reward, next_state, done))
        
        self.t_step = (self.t_step + 1) % self.config.training.update_every
        if self.t_step == 0 and len(self.memory) > self.config.training.pretrain_steps:
            experiences = self.memory.sample()
            self.learn(experiences)

    def act(self, state: np.ndarray, training: bool = True) -> int:
        state = torch.from_numpy(state).float().unsqueeze(0).to(self.device)
        self.qnetwork_local.eval()
        with torch.no_grad():
            action_values = self.qnetwork_local(state)
        self.qnetwork_local.train()

        if training and random.random() > self.epsilon:
            return np.argmax(action_values.cpu().data.numpy())
        else:
            return random.choice(np.arange(self.config.model.action_dim))

    def learn(self, experiences: Tuple) -> None:
        states, actions, rewards, next_states, dones, weights, indices = experiences
        
        # Convert to tensors
        states = torch.FloatTensor(states).to(self.device)
        actions = torch.LongTensor(actions).to(self.device)
        rewards = torch.FloatTensor(rewards).to(self.device)
        next_states = torch.FloatTensor(next_states).to(self.device)
        dones = torch.FloatTensor(dones).to(self.device)
        weights = torch.FloatTensor(weights).to(self.device)
        
        # Distributional DQN
        with torch.cuda.amp.autocast(enabled=self.config.system.mixed_precision):
            # Current Q values
            current_dist = self.qnetwork_local(states)
            current_qs = current_dist[range(self.config.training.batch_size), actions]
            
            # Target Q values
            with torch.no_grad():
                if self.config.model.double_dqn:
                    next_actions = self.qnetwork_local(next_states).argmax(1)
                    target_dist = self.qnetwork_target(next_states)
                    target_qs = target_dist[range(self.config.training.batch_size), next_actions]
                else:
                    target_dist = self.qnetwork_target(next_states)
                    target_qs = target_dist.max(1)[0]
                    
                target_qs = rewards + (self.config.training.gamma ** self.config.system.n_step) * target_qs * (1 - dones)
            
            # Calculate loss
            loss = self._compute_distributional_loss(current_qs, target_qs, weights)
            
        # Optimize
        self.scaler.scale(loss).backward()
        self.scaler.step(self.optimizer)
        self.scaler.update()
        self.optimizer.zero_grad()
        
        # Update target network
        self.soft_update(self.qnetwork_local, self.qnetwork_target)
        
        # Update priorities
        errors = (current_qs - target_qs).abs().cpu().numpy()
        self.memory.update_priorities(indices, errors)
        
        # Log metrics
        self.writer.add_scalar('Loss/train', loss.item(), self.t_step)
        self.epsilon = max(self.config.training.epsilon.end, 
                         self.config.training.epsilon.decay * self.epsilon)

    def soft_update(self, local_model: nn.Module, target_model: nn.Module) -> None:
        for target_param, local_param in zip(target_model.parameters(), local_model.parameters()):
            target_param.data.copy_(self.config.training.tau * local_param.data + 
                                  (1.0 - self.config.training.tau) * target_param.data)

    def save_checkpoint(self, path: str) -> None:
        checkpoint = {
            'qnetwork_local': self.qnetwork_local.state_dict(),
            'qnetwork_target': self.qnetwork_target.state_dict(),
            'optimizer': self.optimizer.state_dict(),
            'epsilon': self.epsilon,
            'timestamp': datetime.now().isoformat(),
            'config': OmegaConf.to_container(self.config)
        }
        torch.save(checkpoint, path)
        
    @classmethod
    def load_checkpoint(cls, path: str) -> 'EnterpriseDQNAgent':
        checkpoint = torch.load(path)
        config = DQNConfig()
        config.config = OmegaConf.create(checkpoint['config'])
        agent = cls(config)
        agent.qnetwork_local.load_state_dict(checkpoint['qnetwork_local'])
        agent.qnetwork_target.load_state_dict(checkpoint['qnetwork_target'])
        agent.optimizer.load_state_dict(checkpoint['optimizer'])
        agent.epsilon = checkpoint['epsilon']
        return agent

# Training Orchestration
class EnterpriseTrainer:
    def __init__(self, env, config: DQNConfig):
        self.env = env
        self.config = config
        self.agent = EnterpriseDQNAgent(config)
        self.logger = logging.getLogger("dqn_trainer")
        
    def train(self) -> None:
        scores = []
        scores_window = deque(maxlen=100)
        
        for i_episode in range(1, self.config.training.num_episodes+1):
            state = self.env.reset()
            score = 0
            for t in range(self.config.training.max_steps):
                action = self.agent.act(state)
                next_state, reward, done, _ = self.env.step(action)
                self.agent.step(state, action, reward, next_state, done)
                state = next_state
                score += reward
                if done:
                    break
                    
            scores_window.append(score)
            scores.append(score)
            
            # Log progress
            self.agent.writer.add_scalar("Reward/train", score, i_episode)
            if i_episode % 100 == 0:
                avg_score = np.mean(scores_window)
                self.logger.info(f"Episode {i_episode} | Average Score: {avg_score:.2f}")
                self.agent.save_checkpoint(f"checkpoint_ep{i_episode}.pth")
                
        # Save final model
        self.agent.save_checkpoint("final_model.pth")
        self._plot_training(scores)
        
    def evaluate(self, num_episodes: int = 10) -> None:
        eval_scores = []
        for i in range(num_episodes):
            state = self.env.reset()
            score = 0
            done = False
            while not done:
                action = self.agent.act(state, training=False)
                state, reward, done, _ = self.env.step(action)
                score += reward
            eval_scores.append(score)
        self.logger.info(f"Evaluation Completed | Average Score: {np.mean(eval_scores):.2f}")

    def _plot_training(self, scores: list) -> None:
        plt.plot(np.arange(len(scores)), scores)
        plt.xlabel('Episode #')
        plt.ylabel('Score')
        plt.savefig('training_progress.png')
        plt.close()

# Production Deployment
class DQNService:
    def __init__(self, model_path: str):
        self.agent = EnterpriseDQNAgent.load_checkpoint(model_path)
        self.agent.qnetwork_local.eval()
        
    def predict_action(self, state: np.ndarray) -> int:
        with torch.no_grad():
            return self.agent.act(state, training=False)
            
    def export_onnx(self, path: str) -> None:
        dummy_input = torch.randn(1, self.agent.config.model.state_dim).to(self.agent.device)
        torch.onnx.export(
            self.agent.qnetwork_local,
            dummy_input,
            path,
            opset_version=13,
            input_names=['state'],
            output_names=['q_values'],
            dynamic_axes={
                'state': {0: 'batch_size'},
                'q_values': {0: 'batch_size'}
            }
        )

# Execution Example
if __name__ == "__main__":
    # Initialize environment and configuration
    config = DQNConfig()
    env = ...  # Your custom environment
    
    # Train the agent
    trainer = EnterpriseTrainer(env, config)
    trainer.train()
    trainer.evaluate()
    
    # Deploy service
    service = DQNService("final_model.pth")
    service.export_onnx("production_model.onnx")
